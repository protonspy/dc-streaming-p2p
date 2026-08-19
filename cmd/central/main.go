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

	"github.com/protonspy/dc-streaming-p2p/internal/auth"
	"github.com/protonspy/dc-streaming-p2p/internal/buildinfo"
	"github.com/protonspy/dc-streaming-p2p/internal/config"
	"github.com/protonspy/dc-streaming-p2p/internal/ice"
	"github.com/protonspy/dc-streaming-p2p/internal/registry"
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

	// Expiry runs for as long as the server does: a peer that stopped answering
	// and that nothing asks about still has to leave the registry and the counts.
	go peers.Sweep(ctx, cfg.ExpiryInterval, func(removed int) {
		logger.InfoContext(ctx, "peers expired", slog.Int("removed", removed))
	})

	iceProvider, err := ice.NewProvider(ice.Options{
		STUNURLs:      cfg.STUNURLs,
		TURNURLs:      cfg.TURNURLs,
		Secret:        cfg.TURNSecret,
		CredentialTTL: cfg.TURNCredentialTTL,
	})
	if err != nil {
		return err
	}

	handler := transport.Chain(routes(cfg, clients, issuer, limiter, peers, iceProvider, logger),
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
	iceProvider *ice.Provider,
	logger *slog.Logger,
) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /healthz", transport.Health(transport.HealthDeps{
		PeersOnline:     func() int { return peers.Count().Online },
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
