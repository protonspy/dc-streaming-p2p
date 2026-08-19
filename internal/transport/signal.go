package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/protonspy/dc-streaming-p2p/internal/auth"
	"github.com/protonspy/dc-streaming-p2p/internal/signaling"
)

// Subprotocol is what a signaling connection speaks. The client offers it
// alongside its token and the server selects it.
const Subprotocol = "dc-signal.v1"

// tokenPrefix is how the token rides in the subprotocol list. A browser cannot set
// headers on a WebSocket, which leaves the query string or this; a token in a query
// string lands in access logs, proxy logs and browser history, and this does not.
const tokenPrefix = "dc-token."

// Defaults for a signaling connection.
const (
	// defaultMaxMessage is the largest signaling message accepted. A session
	// description with a long candidate list is a few kilobytes; anything past
	// this is not signaling.
	defaultMaxMessage = 128 << 10
	// defaultPingInterval is how often the server pings, and defaultPongTimeout
	// how long it waits for the answer before closing the channel.
	defaultPingInterval = 20 * time.Second
	defaultPongTimeout  = 10 * time.Second
)

// SignalDeps is what the signaling endpoint needs.
type SignalDeps struct {
	// Hub holds the connections and routes between them.
	Hub *signaling.Hub
	// Verifier checks the token offered in the subprotocol.
	Verifier *auth.TokenVerifier
	// AllowedOrigins are the browser origins that may connect.
	AllowedOrigins []string
	// AllowAnyOrigin drops the origin check. It is separate from an empty
	// AllowedOrigins on purpose: allowing everybody has to be something somebody
	// asked for, not what happens when a setting is missing.
	AllowAnyOrigin bool
	// MaxMessageBytes bounds one message. Zero takes the default.
	MaxMessageBytes int64
	// PingInterval and PongTimeout are the connection-level heartbeat: a peer
	// that stops answering loses its channel, which is what keeps a wedged
	// process from looking online forever.
	PingInterval time.Duration
	PongTimeout  time.Duration
	// Logger records connections and closures without recording payloads.
	Logger *slog.Logger
}

// Signal serves GET /signal: it verifies the token, upgrades, and hands the
// connection to the hub for as long as it lasts.
//
// Authentication happens before the upgrade, so a caller with no token gets an
// ordinary 401 rather than a WebSocket that closes immediately — the difference
// matters to anything that is not a browser.
func Signal(deps SignalDeps) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxMessage := deps.MaxMessageBytes
	if maxMessage <= 0 {
		maxMessage = defaultMaxMessage
	}
	pingInterval := deps.PingInterval
	if pingInterval <= 0 {
		pingInterval = defaultPingInterval
	}
	pongTimeout := deps.PongTimeout
	if pongTimeout <= 0 {
		pongTimeout = defaultPongTimeout
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, found := tokenFromSubprotocols(r)
		if !found {
			writeTokenError(w, http.StatusUnauthorized, codeInvalidToken)
			return
		}

		claims, err := deps.Verifier.Verify(raw)
		if err != nil {
			code := codeInvalidToken
			if errors.Is(err, auth.ErrTokenExpired) {
				code = codeTokenExpired
			}
			writeTokenError(w, http.StatusUnauthorized, code)
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:       []string{Subprotocol},
			OriginPatterns:     deps.AllowedOrigins,
			InsecureSkipVerify: deps.AllowAnyOrigin,
		})
		if err != nil {
			// Accept has already answered; the reason is ours to log.
			logger.InfoContext(r.Context(), "signaling upgrade refused",
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("peer_id", claims.PeerID),
				slog.String("error", err.Error()),
			)
			return
		}
		conn.SetReadLimit(maxMessage)

		channel := &wsConn{conn: conn, logger: logger}
		deps.Hub.Register(claims.PeerID, channel)
		defer func() {
			deps.Hub.Deregister(claims.PeerID, channel)
			_ = conn.CloseNow()
		}()

		logger.InfoContext(r.Context(), "signaling channel open",
			slog.String("request_id", RequestIDFrom(r.Context())),
			slog.String("peer_id", claims.PeerID),
		)

		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()

		go keepAlive(ctx, conn, pingInterval, pongTimeout)

		readLoop(ctx, conn, deps.Hub, claims.PeerID, logger)
	})
}

// readLoop carries messages from one connection into the hub until it closes.
func readLoop(ctx context.Context, conn *websocket.Conn, hub *signaling.Hub, peerID string, logger *slog.Logger) {
	for {
		var msg signaling.Message
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			status := websocket.CloseStatus(err)
			logger.InfoContext(ctx, "signaling channel closed",
				slog.String("peer_id", peerID),
				slog.Int("close_status", int(status)),
			)
			return
		}

		// The type and the session are all the server reads. What it refuses, it
		// answers on the same channel rather than by closing it.
		if err := hub.Route(ctx, peerID, msg); err != nil {
			logger.InfoContext(ctx, "signaling message not routed",
				slog.String("peer_id", peerID),
				slog.String("type", string(msg.Type)),
				slog.String("error", err.Error()),
			)
			return
		}
	}
}

// keepAlive pings the far side and closes the channel when it stops answering. A
// peer whose process is wedged holds a socket that looks perfectly healthy from
// the outside, and this is what tells the difference.
func keepAlive(ctx context.Context, conn *websocket.Conn, interval, timeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, timeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				_ = conn.Close(websocket.StatusPolicyViolation, "no answer to the heartbeat")
				return
			}
		}
	}
}

// tokenFromSubprotocols pulls the token out of the offered subprotocols.
func tokenFromSubprotocols(r *http.Request) (string, bool) {
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, offered := range strings.Split(header, ",") {
			offered = strings.TrimSpace(offered)
			if token, found := strings.CutPrefix(offered, tokenPrefix); found && token != "" {
				return token, true
			}
		}
	}
	return "", false
}

// wsConn is one WebSocket as the hub uses it. The write lock is what makes it safe
// for the reading goroutine and a delivering goroutine to share: a WebSocket
// connection cannot have two writers at once.
type wsConn struct {
	conn   *websocket.Conn
	logger *slog.Logger

	mu sync.Mutex
}

// Send writes one message, bounded by whatever deadline the caller's context
// carries.
func (c *wsConn) Send(ctx context.Context, msg signaling.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return wsjson.Write(ctx, c.conn, msg)
}

// Close ends the connection, saying why in the close frame so the far side can
// tell a replacement apart from a failure.
func (c *wsConn) Close(reason string) {
	// Close frames carry at most 123 bytes of reason.
	if len(reason) > 123 {
		reason = reason[:123]
	}
	_ = c.conn.Close(websocket.StatusNormalClosure, reason)
}
