---
autonomy: auto
ci: wait
---

# P2P federation

A second kind of node: a peer server anyone can run inside their own network, and
an anchor that keeps a registry of the peers around it. Anchors know each other and
forward a question they cannot answer themselves, and a peer that loses its central
server finds another from a list rather than going dark. A relay that dies hands its traffic to the least loaded
peer server that can take it.

## Why

`plans/p2p-streaming.md` puts the whole control plane in one process: one registry,
one signaling path, one thing to lose. That is the right first system and the wrong
final one, because every peer in it depends on a server none of them run. Federation
moves the registry to the edge — several anchors, each holding only the peers near
it and forwarding what it cannot answer — so that losing any one of them costs
reconnection rather than the call. Nothing is replicated, so nothing can disagree:
an anchor either holds a peer or asks the ones it knows. It is finished when a peer whose anchor is killed re-registers
with another from its list and completes a call without the application restarting,
and when a relay killed mid-transmission is replaced without the sender stopping.

## Paths

- `cmd/anchor/` — the anchor binary
- `internal/federation/` — the membership list, the gossip protocol, reconciliation
- `internal/relaypool/` — relay candidates, their load, and the choice between them
- `web/sdk/` — the parallel probe and the choice a client makes from its results
- `internal/registry/` — grows a remote half beside the local one

## References

- `specs/anchor-node/` — the anchor: what it registers, what it holds, and how a peer server inside a network reaches it
- `specs/anchor-membership/` — the list of known anchors, how a new one joins from it, and how a dead one leaves it
- `specs/registry-forwarding/` — an anchor asked for a peer it does not hold: who it asks, how far a question travels, and what it caches
- `specs/control-plane-failover/` — a peer that loses its server: detection, the next candidate, and re-registration without dropping the call
- `specs/relay-failover/` — replacing a relay mid-transmission, and choosing the least loaded peer server that can take it
- `specs/session-migration/` — a server that dies mid-transmission: naming its successor, telling the clients that were on it, and continuing the stream on the new one
- `specs/anchor-selection/` — probing the known anchors at once under a deadline and picking the one that answers fastest and carries least, which becomes that session's coordinator

## Out of scope

- Replacing the central server: it stays the coordinator, and federation is what happens when one is unreachable
- Replicating the registry: an anchor holds its own peers and forwards the rest
- Trusting a peer server by the fact that it runs — an anchor without the deployment secret is not admitted
- Broadcast trees and 1:N distribution, which are a different decomposition and a different plan
- Anything in `plans/p2p-streaming.md`, which has to land first: this plan has no meaning without a working single-node control plane

## Tasks

- [ ] 1.1 (Unit) Write the ADR recording lookup by forwarding rather than replication, and the deployment secret that admits an anchor
  _Priority 1_
- [ ] 1.2 (Unit) Write the ADR superseding the single-node registry decision once federation lands
  _Depends 1.1_
- [ ] 1.3 (Unit) Record the wire protocol between anchors in the wiki, before either side is written
  _Depends 1.1_
- [ ] 1.4 (Unit) Record what an anchor reports as its load, and why a client can believe a number the anchor itself chose
  _Depends 1.1_

## Done when

- A peer whose anchor is stopped re-registers with the next anchor on its list, and a call in progress survives it
- An anchor asked for a peer it does not hold answers from another anchor, and a question that no anchor can answer ends rather than circulating
- A relay stopped mid-transmission is replaced by another peer server without the sender restarting the call
- A client given several anchors probes them at once, chooses one inside its deadline, and chooses a different one when the first is slow or loaded
- A server carrying a transmission is stopped, another peer server is named in its place, and every client that was on the old one continues on the named successor
- An anchor nobody authorized cannot enter the membership list of another
