# Stack

Every adopted technology, with one line on why it earned its place. Technology that
is not listed here is an open decision, never something adopted silently.

## Runtime

- **Go** — one long-lived connection per online peer is a concurrency problem, and a
  goroutine per connection is the cheapest way to hold it. See
  `adr:0002-use-go-for-the-central-server`.
- **net/http, from the standard library** — routing with method patterns has been in
  the standard library since Go 1.22, and the control plane's surface is small enough
  that a framework would be the larger dependency.
- **log/slog, from the standard library** — structured logs with no dependency, and
  the handler interface is what lets the tests read the log as data.
- **WebRTC, in the browser** — the media path is the browser's own implementation;
  the server holds no track and no peer connection. See
  `adr:0001-split-control-plane-from-data-plane`.
- **coturn, external** — the relay for calls with no direct path, configured rather
  than implemented, and fed ephemeral credentials this server signs. See
  `adr:0003-use-an-external-turn-service-with-ephemeral-credentials`.

## Development

- **go test** — the suite CI gates on; no assertion library, because a failure
  message written by hand says what the requirement was.
- **golangci-lint** — the best-practices layer the tests do not cover: unchecked
  errors, unclosed bodies, requests built without a context. Configured in
  `.golangci.yml`.
- **gofmt and go vet** — the format and the correctness checks that ship with the
  toolchain, run locally before a task closes and again in CI.
- **GitHub Actions** — CI on every push and pull request, in `.github/workflows/ci.yml`.
- **@protonspy/scc** — grades the specs, the plan, and this knowledge base; CI pins
  the version so a new release cannot turn a green branch red on its own.
