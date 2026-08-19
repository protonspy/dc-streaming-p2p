// Command central runs the control plane: authentication, the peer registry,
// session records, and the signaling that lets two peers negotiate a peer
// connection. It never carries media.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/auth"
	"github.com/protonspy/dc-streaming-p2p/internal/buildinfo"
	"github.com/protonspy/dc-streaming-p2p/internal/config"
	"github.com/protonspy/dc-streaming-p2p/internal/ice"
	"github.com/protonspy/dc-streaming-p2p/internal/registry"
	"github.com/protonspy/dc-streaming-p2p/internal/session"
	"github.com/protonspy/dc-streaming-p2p/internal/signaling"
	"github.com/protonspy/dc-streaming-p2p/internal/transport"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "central:", err)
		os.Exit(1)
	}
}

// run is main with its process concerns passed in, so a test can call it: the
// arguments, the environment, and where output goes.
func run(ctx context.Context, args []string, getenv config.Getenv, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("central", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print the build and exit")
	logFormat := flags.String("log", "json", "log format: json or text")
	if err := flags.Parse(args); err != nil {
		// Asking for the usage is not a failure: the flag package has already
		// printed it, and exiting non-zero for it breaks anything that shells out.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *showVersion {
		fmt.Fprintln(stdout, buildinfo.String())
		return nil
	}

	logger, err := newLogger(stderr, *logFormat)
	if err != nil {
		return err
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "starting", slog.String("build", buildinfo.String()), slog.String("config", cfg.String()))

	clients, err := auth.NewClientStore(cfg.Clients)
	if err != nil {
		return err
	}
	issuer, err := auth.NewTokenIssuer(cfg.TokenSigningKey, cfg.Issuer, cfg.TokenTTL)
	if err != nil {
		return err
	}
	limiter, err := auth.NewAttemptLimiter(cfg.AuthMaxFailures, cfg.AuthFailureWindow)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "authentication ready",
		slog.Int("clients", clients.Len()),
		slog.String("key_id", transport.KeyID(issuer.PublicKey())),
	)

	peers, err := registry.New(registry.Options{
		HeartbeatInterval: cfg.HeartbeatInterval,
		SuspectAfter:      cfg.SuspectAfter,
		OfflineAfter:      cfg.OfflineAfter,
		MaxPeers:          cfg.MaxPeers,
	})
	if err != nil {
		return err
	}

	sessions, err := session.New(session.Options{
		NegotiationTimeout: cfg.NegotiationTimeout,
		Retention:          cfg.SessionRetention,
		MaxSessions:        cfg.MaxSessions,
		MaxSessionsPerPeer: cfg.MaxSessionsPerPeer,
		Peers:              reachable{peers},
	})
	if err != nil {
		return err
	}

	hub, err := signaling.New(signaling.Options{
		Sessions:          sessionsAsPairs{sessions},
		Presence:          presence{peers},
		MessagesPerWindow: cfg.SignalMessagesPerWindow,
		Window:            cfg.SignalWindow,
	})
	if err != nil {
		return err
	}

	// A peer holding a channel is a peer that is there, so the registry is told
	// so on its own interval rather than making the peer report in twice.
	go refreshPresence(ctx, hub, peers, cfg.HeartbeatInterval)

	// Expiry runs for as long as the server does: a peer that stopped answering
	// and that nothing asks about still has to leave the registry and the counts —
	// and every session it was in has to end, since the far side is waiting on a
	// peer that is not there.
	go peers.Sweep(ctx, cfg.ExpiryInterval, func(removed []string) {
		var closed int
		for _, peerID := range removed {
			for _, ended := range sessions.ClosePeer(peerID) {
				closed++
				// The peer still holding a channel is told, rather than left
				// waiting on somebody who is gone.
				hub.SessionEnded(ctx, ended.ID, string(ended.State), ended.Caller, ended.Callee)
			}
		}
		logger.InfoContext(ctx, "peers expired",
			slog.Int("peers", len(removed)),
			slog.Int("sessions_closed", closed),
		)
	})

	// Sessions settle on read, so this only bounds the memory: it fails what has
	// been negotiating too long and forgets what is past its retention.
	go sweepSessions(ctx, sessions, cfg.ExpiryInterval, logger)

	iceProvider, err := ice.NewProvider(ice.Options{
		STUNURLs:      cfg.STUNURLs,
		TURNURLs:      cfg.TURNURLs,
		Secret:        cfg.TURNSecret,
		CredentialTTL: cfg.TURNCredentialTTL,
	})
	if err != nil {
		return err
	}

	handler := transport.Chain(routes(cfg, clients, issuer, limiter, peers, sessions, hub, iceProvider, logger),
		transport.WithRequestID, transport.WithLogging(logger))
	srv, err := transport.NewServer(ctx, handler, transport.Options{
		Addr:            cfg.ListenAddr,
		ShutdownTimeout: cfg.ShutdownTimeout,
		Logger:          logger,
	})
	if err != nil {
		return err
	}

	return srv.Run(ctx)
}

// routes is the control plane's HTTP surface. It grows one endpoint at a time as
// the specs land; until then it answers nothing but its own liveness.
//
// The health counters are wired as they arrive: until the registry and the session
// store exist, the endpoint reports the zeroes that are the truth.
func routes(
	cfg config.Config,
	clients *auth.ClientStore,
	issuer *auth.TokenIssuer,
	limiter *auth.AttemptLimiter,
	peers *registry.Registry,
	sessions *session.Store,
	hub *signaling.Hub,
	iceProvider *ice.Provider,
	logger *slog.Logger,
) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /healthz", transport.Health(transport.HealthDeps{
		PeersOnline:     func() int { return peers.Count().Online },
		SessionsLive:    sessions.Count,
		ChannelsOpen:    hub.Count,
		RelayConfigured: cfg.RelayConfigured(),
		Build:           buildinfo.String(),
	}))

	// Both of these are unauthenticated, and have to be: one is where a peer gets
	// its token, and the other is what it pins before it will send a credential.
	mux.Handle("POST /auth", transport.Auth(transport.AuthDeps{
		Clients: clients,
		Issuer:  issuer,
		Limiter: limiter,
		Logger:  logger,
	}))
	mux.Handle("GET /.well-known/jwks.json", transport.JWKS(issuer.PublicKey()))

	// Everything below here is a peer acting as itself, so the token is the
	// identity and the middleware is what supplies it.
	verifier := auth.NewTokenVerifier(issuer.PublicKey(), cfg.Issuer)
	guard := transport.RequireToken(verifier)

	mux.Handle("POST /peers", guard(transport.Peers(peers)))
	mux.Handle("DELETE /peers", guard(transport.Peers(peers)))
	mux.Handle("POST /peers/heartbeat", guard(transport.Heartbeat(peers)))
	mux.Handle("GET /peers/{id}", guard(transport.PeerLookup(peers)))
	mux.Handle("GET /ice-config", guard(transport.ICEConfig(iceProvider)))

	mux.Handle("POST /sessions", guard(transport.OpenSession(sessions)))
	mux.Handle("GET /sessions/{id}", guard(transport.GetSession(sessions, hub)))
	mux.Handle("DELETE /sessions/{id}", guard(transport.GetSession(sessions, hub)))
	mux.Handle("POST /sessions/{id}/state", guard(transport.ReportSession(sessions, hub)))

	// The signaling channel authenticates itself: a browser cannot set headers on
	// a WebSocket, so the token rides in the subprotocol and is verified before
	// the upgrade.
	mux.Handle("GET /signal", transport.Signal(transport.SignalDeps{
		Hub:             hub,
		Verifier:        verifier,
		AllowedOrigins:  cfg.AllowedOrigins,
		MaxMessageBytes: cfg.MaxSignalMessageBytes,
		Logger:          logger,
	}))

	return mux
}

// newLogger builds the process logger. Logs go to standard error so that standard
// output stays available for anything a person asked for, such as the version.
func newLogger(w io.Writer, format string) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("unknown log format %q, want json or text", format)
	}
}

// reachable adapts the registry to what the session store asks of it: whether a
// peer can be reached at all. The session store does not need the record, only the
// answer, so this is the whole of the coupling between them.
type reachable struct {
	peers *registry.Registry
}

func (r reachable) Lookup(peerID string) bool {
	_, held := r.peers.Lookup(peerID)
	return held
}

// sweepSessions fails what has negotiated too long and forgets what is past its
// retention, until ctx is cancelled.
func sweepSessions(ctx context.Context, sessions *session.Store, every time.Duration, logger *slog.Logger) {
	if every <= 0 {
		every = time.Minute
	}

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			failed, forgotten := sessions.Reap()
			if failed > 0 || forgotten > 0 {
				logger.InfoContext(ctx, "sessions swept",
					slog.Int("failed", failed),
					slog.Int("forgotten", forgotten),
				)
			}
		}
	}
}

// sessionsAsPairs adapts the session store to the one question the hub asks: who
// is the other side of this session, and is the asking peer in it at all.
type sessionsAsPairs struct {
	sessions *session.Store
}

func (s sessionsAsPairs) Other(sessionID, peerID string) (string, bool) {
	held, err := s.sessions.Get(sessionID, peerID)
	if err != nil || !held.Live() {
		return "", false
	}
	return held.Other(peerID), true
}

// presence adapts the registry to what the hub reports: a peer opening a channel
// is a peer that is here.
type presence struct {
	peers *registry.Registry
}

func (p presence) Connected(peerID string) {
	if _, err := p.peers.Heartbeat(peerID); err != nil {
		_, _ = p.peers.Register(peerID)
	}
}

func (p presence) Disconnected(_ string) {
	// Nothing. A peer that closed its channel may still be there and still
	// reporting in; the registry's own window is what decides that.
}

// refreshPresence keeps every peer holding a channel online, so a connected peer
// does not have to report in on two mechanisms at once.
func refreshPresence(ctx context.Context, hub *signaling.Hub, peers *registry.Registry, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, peerID := range hub.Peers() {
				if _, err := peers.Heartbeat(peerID); err != nil {
					_, _ = peers.Register(peerID)
				}
			}
		}
	}
}
