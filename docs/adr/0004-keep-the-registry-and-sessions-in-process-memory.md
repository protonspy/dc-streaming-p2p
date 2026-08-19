---
status: accepted
---

# Keep the registry and sessions in process memory

## Context

The registry — which peers are online, when each was last seen — and the session
records are read and written on every heartbeat and every signaling message. They are
also disposable: a peer that reconnects registers again, and a session whose peers are
gone has no value.

Sharing them in Redis buys horizontal scaling and survives a restart. It also adds a
network hop on the hottest path, a second thing to operate, and publish-subscribe
routing so two peers connected to different instances can still signal each other.

## Decision

Both live in a map in the process, behind the store interfaces the packages already
define. One node. Nothing is persisted.

## Consequences

The read path is a mutex and a map lookup, and development needs no infrastructure
beyond the binary.

A restart drops every control-plane connection and empties the registry. Peers
reconnect and register again; calls already established keep running, because the
media path does not depend on the server — that is
`adr:0001-split-control-plane-from-data-plane` paying out. Calls in the middle of
negotiating fail and have to be retried.

The ceiling is one machine, and there is no failover. Going past that means a shared
store and cross-instance routing, which is a new decision that supersedes this one
rather than extending it. Keeping the store behind an interface is what makes that a
replaceable implementation instead of a rewrite.
