package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Server runs the HTTP surface and stops it without cutting live requests off.
type Server struct {
	http     *http.Server
	listener net.Listener
	logger   *slog.Logger
	shutdown time.Duration
}

// Options are what a Server needs that is not the handler itself.
type Options struct {
	// Addr is the host:port to bind.
	Addr string
	// ShutdownTimeout bounds the wait for in-flight requests once a stop is asked
	// for. Past it, the remaining connections are closed.
	ShutdownTimeout time.Duration
	// ReadHeaderTimeout bounds how long a client may take to send its headers.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds the whole request, headers and body together, and
	// WriteTimeout bounds the response. Together they are what stops a client
	// that dribbles a body or reads a response one byte at a time from holding a
	// connection and its goroutine open for as long as it likes.
	//
	// Both are deadlines on the underlying connection, so a handler that hijacks
	// it — the WebSocket upgrade does — has to clear them itself. The control
	// plane's connections are long-lived and idle by design; that is a property of
	// a hijacked connection, not a reason to leave the HTTP surface unbounded.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// IdleTimeout bounds a kept-alive connection between requests.
	IdleTimeout time.Duration
	// Logger receives the server's own lifecycle lines.
	Logger *slog.Logger
}

// Defaults applied when Options leaves a timeout unset. They are generous for a
// control plane whose largest request is a session description, and they exist to
// bound an abusive client rather than to discipline a slow one.
const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 120 * time.Second
)

// NewServer binds the address immediately, so a port already in use is an error
// here rather than a failure moments after the process reports itself started. The
// context governs the bind itself, not the serving that follows: that is Run.
func NewServer(ctx context.Context, handler http.Handler, opts Options) (*Server, error) {
	if handler == nil {
		return nil, errors.New("transport: no handler to serve")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	readHeader := orDefault(opts.ReadHeaderTimeout, defaultReadHeaderTimeout)
	read := orDefault(opts.ReadTimeout, defaultReadTimeout)
	write := orDefault(opts.WriteTimeout, defaultWriteTimeout)
	idle := orDefault(opts.IdleTimeout, defaultIdleTimeout)

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("transport: listen on %q: %w", opts.Addr, err)
	}

	return &Server{
		http: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: readHeader,
			ReadTimeout:       read,
			WriteTimeout:      write,
			IdleTimeout:       idle,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		},
		listener: listener,
		logger:   logger,
		shutdown: opts.ShutdownTimeout,
	}, nil
}

// Addr is the address actually bound, which is the resolved one when the
// configured port was 0.
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// Run serves until ctx is cancelled, then drains. It returns nil on a clean stop
// and an error only when serving or draining failed.
//
// The control plane holds long-lived WebSocket connections, so a drain that hits
// its deadline is expected rather than exceptional: those connections are closed
// and the peers reconnect.
func (s *Server) Run(ctx context.Context) error {
	errs := make(chan error, 1)
	go func() {
		s.logger.Info("serving", slog.String("addr", s.Addr()))
		err := s.http.Serve(s.listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	s.logger.Info("draining", slog.Duration("timeout", s.shutdown))
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdown)
	defer cancel()

	shutdownErr := s.http.Shutdown(drainCtx)
	serveErr := <-errs

	if errors.Is(shutdownErr, context.DeadlineExceeded) {
		s.logger.Warn("drain timed out, closing what was left")
		shutdownErr = s.http.Close()
	}
	return errors.Join(serveErr, shutdownErr)
}

// orDefault takes the configured duration, or the default when nothing usable was
// configured.
func orDefault(configured, fallback time.Duration) time.Duration {
	if configured <= 0 {
		return fallback
	}
	return configured
}
