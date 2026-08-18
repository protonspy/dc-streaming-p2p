---
status: accepted
---

# Use Go for the central server

## Context

The control plane holds one long-lived WebSocket per online peer, plus timers for
heartbeats and session expiry. That is a concurrency problem before it is a
computation problem: many mostly-idle connections, each waking on a message.

The candidates were Go, Python with asyncio, Node with TypeScript, and Rust. The
repository was already scaffolded with a Go `.gitignore`.

## Decision

Go, with the standard library HTTP server. The WebRTC and TURN ecosystem in Go is
`pion`, which is available should the relay decision in
`adr:0003-use-an-external-turn-service-with-ephemeral-credentials` ever be revisited.

## Consequences

A goroutine per connection fits the connection-per-peer shape, and the deployable
artifact is a single binary with no runtime to install.

The browser SDK is TypeScript regardless, so the project carries two languages.
Anything shared across the control-plane protocol — message envelopes, session state —
is declared twice and can drift; the protocol tests are what catch that.

Rust would have a better profile for a relay we ran ourselves. Since we do not run
one, that advantage buys nothing here.
