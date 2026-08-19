# This project

A WebRTC control plane in Go plus a browser SDK. The server signals; media travels
peer to peer. See `plans/p2p-streaming.md` for the decomposition.

## Commands

There is no `make` on this machine and no task runner. These are the commands.

```bash
# Build
go build ./...

# Test — the whole suite
go test ./...

# Test — one package or one file (used after every task; scope, not suite)
go test ./internal/config/...
go test -run TestLoadRejectsAMissingSigningKey ./internal/config/

# Lint — the best-practices layer that finds what tests do not
go vet ./...

# Format / format check
gofmt -w .
gofmt -l .        # prints the files that are not formatted; empty output is the pass
```

`golangci-lint` is not installed locally; CI runs it and it is the gate before merge.
Locally `go vet ./...` plus `gofmt -l .` is what a task closes on.

```bash
# The browser SDK — plain ES modules, no install step
node --test "web/sdk/*.test.js"
```

## Conventions

- **Branch names:** `feat/<slug>`, `fix/<slug>` — one per group of the plan, or per spec
- **Commits:** Conventional Commits, scoped by package — `feat(registry): …`
- **Vocabulary:** `docs/glossary.md` is binding in code as well as in prose. A peer has
  a *peer identifier*; a *token* is not an identity; the *relay* is the external TURN
  service and never this server.
- **What a new contributor gets wrong first:** putting media anywhere near the server.
  The central server holds no track, no stream, and no `RTCPeerConnection` — see
  `adr:0001-split-control-plane-from-data-plane`.

## Boundaries

- `.codegraph/` is CodeGraph's index — never edited, never committed.
- `plans/p2p-streaming.md` is approved and sealed: tick boxes with `scc patch check`,
  and add or remove tasks only with a `--reason`. Never edit it in an editor.
- ADRs under `docs/adr/` are appended to and superseded, never rewritten.
