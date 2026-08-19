---
autonomy: auto
ci: wait
status: approved
checksum: 499838a95ab98d22b8fc274d5817c6a2286ba9ea1f0f364db64c695cadf57931
---

# P2P streaming

A central server that authenticates peers, keeps a registry of who is online,
authorizes and tracks sessions, and carries the WebRTC signaling between two
browsers. Audio and video travel peer to peer; an external TURN service carries them
only when the direct path fails.

## Why

The expensive half of a call is the media, and the server has no reason to touch it.
Splitting the system into a control plane the server owns and a data plane the peers
own keeps the server's cost flat as calls are added, and leaves TURN as the exception
rather than the path. The initiative is finished when two browsers on different
networks exchange audio and video through the SDK, when a blocked direct path falls
back to the relay without the application seeing a difference, and when the server
survives a peer disappearing without a goodbye.

## Paths

- `cmd/central/` — the server binary
- `internal/auth/` · `internal/registry/` · `internal/session/` · `internal/signaling/` · `internal/ice/`
- `internal/transport/` — HTTP and WebSocket surface
- `web/sdk/` — the browser SDK and the demo page
- `deploy/` — development compose file, coturn configuration

## References

- `specs/auth-tokens/` — `POST /auth`, client credentials to a short-lived JWT, and token verification on every control-plane connection
- `specs/peer-registry/` — registration, heartbeat, and the ONLINE → SUSPECT → OFFLINE transitions
- `specs/session-lifecycle/` — authorizing peer A to reach peer B, the session record, its states, and its end
- `specs/signaling-channel/` — the WebSocket envelope and the routing of offer, answer, and ICE candidates between two peers
- `specs/ice-config/` — the STUN and TURN configuration handed to a peer, with ephemeral HMAC credentials
- `specs/web-sdk/` — `connect(peerId)` in the browser, media capture, and the P2P-or-relay abstraction

## Out of scope

- Broadcast 1:N, SFU forwarding, and P2P relay trees — the data plane is two peers
- Mesh conferencing beyond two participants
- A TURN implementation of our own; the plan configures an external one
- Horizontal scaling and persistence — registry and sessions live in the process memory of a single node
- Recording, transcoding, and media processing of any kind
- A product user interface; the demo page exists to prove the path, not to ship

## Tasks

- [x] 1.1 (Unit) Initialize the Go module and the cmd/internal layout with a build and test entry point
- [x] 1.2 (Unit) Load and validate configuration at startup — listen address, token signing key, TURN secret and URLs, heartbeat and session timeouts
  _Depends 1.1_
- [x] 1.3 (Unit) Serve HTTP with structured logging, request identifiers, and graceful shutdown
  _Depends 1.2_
- [x] 1.4 (Unit) Expose a health endpoint reporting registry size and live session count
  _Depends 1.3_
- [x] 2.1 (Unit) Serve a two-browser demo page that captures audio and video and calls the SDK
  _Depends 1.3_
- [x] 2.2 (Unit) Add the CI workflow — build, test, vet, and lint on every push
  _Depends 1.1_
- [x] 2.3 (Unit) Add a development compose file running the server against a local coturn
  _Depends 1.2_
- [x] 2.4 (Unit) Record the adopted technology in docs/stack.md and the canonical vocabulary in docs/glossary.md
  _Depends 1.1_

## Done when

- Two browsers on different networks exchange audio and video end to end through the SDK
- With the direct path blocked, the same call completes over TURN and the SDK reports the connection type without the application changing
- A peer that stops sending heartbeats leaves the registry, and any session it held is closed
- Every referenced spec is closed, `scc validate` exits 0, and the suite and lint are green in CI
