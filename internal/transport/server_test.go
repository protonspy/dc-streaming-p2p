package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

// quietLogger keeps the server's lifecycle lines out of the test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startServer binds an ephemeral port and runs the server until the returned stop
// is called, which is also what the test asserts the drain on.
func startServer(t *testing.T, handler http.Handler, shutdown time.Duration) (addr string, stop func() error) {
	t.Helper()

	srv, err := NewServer(context.Background(), handler, Options{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: shutdown,
		Logger:          quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	return srv.Addr(), func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			return errors.New("Run() did not return after its context was cancelled")
		}
	}
}

func TestServerServesAndStops(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("pong"))
	})

	addr, stop := startServer(t, mux, time.Second)

	resp, err := http.Get("http://" + addr + "/ping")
	if err != nil {
		t.Fatalf("GET /ping error = %v, want nil", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK || string(body) != "pong" {
		t.Errorf("GET /ping = %d %q, want 200 \"pong\"", resp.StatusCode, body)
	}

	if err := stop(); err != nil {
		t.Errorf("Run() error = %v, want nil on a clean stop", err)
	}
}

func TestServerDrainsAnInFlightRequest(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})

	mux := http.NewServeMux()
	started := make(chan struct{})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.Write([]byte("done"))
		close(finished)
	})

	addr, stop := startServer(t, mux, 5*time.Second)

	responses := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			t.Errorf("GET /slow error = %v, want nil", err)
			responses <- nil
			return
		}
		responses <- resp
	}()

	<-started

	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()

	close(release)

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight handler was cut off by the shutdown")
	}

	if resp := <-responses; resp != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if err := <-stopped; err != nil {
		t.Errorf("Run() error = %v, want nil after a completed drain", err)
	}
}

func TestServerGivesUpOnADrainThatOutlastsTheTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	mux := http.NewServeMux()
	started := make(chan struct{})
	mux.HandleFunc("GET /stuck", func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	})

	addr, stop := startServer(t, mux, 50*time.Millisecond)

	go func() {
		resp, err := http.Get("http://" + addr + "/stuck")
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-started

	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("Run() error = %v, want nil when the drain timed out and closed what was left", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() never returned; a drain timeout has to give up")
	}
}

func TestNewServerRejectsAnAddressInUse(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer taken.Close()

	_, err = NewServer(context.Background(), http.NewServeMux(), Options{Addr: taken.Addr().String(), Logger: quietLogger()})
	if err == nil {
		t.Error("NewServer() error = nil for an address already bound, want an error")
	}
}

func TestNewServerRejectsNoHandler(t *testing.T) {
	if _, err := NewServer(context.Background(), nil, Options{Addr: "127.0.0.1:0"}); err == nil {
		t.Error("NewServer(nil) error = nil, want an error")
	}
}

func TestServerAddrReportsTheBoundPort(t *testing.T) {
	srv, err := NewServer(context.Background(), http.NewServeMux(), Options{Addr: "127.0.0.1:0", Logger: quietLogger()})
	if err != nil {
		t.Fatalf("NewServer() error = %v, want nil", err)
	}
	t.Cleanup(func() { srv.http.Close() })

	_, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("Addr() = %q, want host:port: %v", srv.Addr(), err)
	}
	if port == "0" {
		t.Errorf("Addr() = %q, want the resolved port rather than 0", srv.Addr())
	}
}

func TestNewServerBoundsEveryPhaseOfARequest(t *testing.T) {
	srv, err := NewServer(context.Background(), http.NewServeMux(), Options{
		Addr:   "127.0.0.1:0",
		Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v, want nil", err)
	}
	t.Cleanup(func() { srv.http.Close() })

	timeouts := map[string]time.Duration{
		"ReadHeaderTimeout": srv.http.ReadHeaderTimeout,
		"ReadTimeout":       srv.http.ReadTimeout,
		"WriteTimeout":      srv.http.WriteTimeout,
		"IdleTimeout":       srv.http.IdleTimeout,
	}
	for name, got := range timeouts {
		if got <= 0 {
			t.Errorf("%s = %s, want a bound — an unbounded phase is a connection a client can hold open forever", name, got)
		}
	}
}

func TestNewServerKeepsTheConfiguredTimeouts(t *testing.T) {
	srv, err := NewServer(context.Background(), http.NewServeMux(), Options{
		Addr:              "127.0.0.1:0",
		Logger:            quietLogger(),
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       4 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v, want nil", err)
	}
	t.Cleanup(func() { srv.http.Close() })

	if srv.http.ReadHeaderTimeout != time.Second || srv.http.ReadTimeout != 2*time.Second ||
		srv.http.WriteTimeout != 3*time.Second || srv.http.IdleTimeout != 4*time.Second {
		t.Errorf("timeouts = %s/%s/%s/%s, want 1s/2s/3s/4s",
			srv.http.ReadHeaderTimeout, srv.http.ReadTimeout, srv.http.WriteTimeout, srv.http.IdleTimeout)
	}
}

func TestServerCutsOffAClientThatStopsSendingItsBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /slow-body", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	})

	srv, err := NewServer(context.Background(), mux, Options{
		Addr:        "127.0.0.1:0",
		ReadTimeout: 150 * time.Millisecond,
		Logger:      quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("net.Dial() error = %v, want nil", err)
	}
	defer conn.Close()

	// Announce a body and never send it.
	request := "POST /slow-body HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write error = %v, want nil", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("the connection was still open after the read timeout: %v", err)
	}
}
