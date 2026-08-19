---
autonomy: auto
ci: wait
---

# Signaling channel — requirements

## Purpose

Two peers cannot describe themselves to each other without help: each has a session
description and a set of candidate addresses that only the other one needs, and
neither can reach the other yet. This feature is that help, and nothing more — the
server carries the negotiation and never the media.

Once the peer connection is up, this channel has done its work. It stays open
because either side may renegotiate, and because a peer that closes it is a peer the
registry should stop hearing from.

## R1 · Connecting

- **R1.1** When an authenticated peer opens the signaling channel, the control plane shall accept the connection and shall register that peer.
- **R1.2** If a connection presents no token, a token this server did not sign, or an expired one, then the control plane shall refuse the connection.
- **R1.3** If a peer opens a second signaling channel while one is open, then the control plane shall close the older connection and keep the newer one.
- **R1.4** The control plane shall refuse a connection from an origin that is not among the configured ones.
- **R1.5** When a signaling channel closes, the control plane shall deregister that peer.
- **R1.6** If no allowed origins are configured, then the server shall refuse to start, and shall accept every origin only where that was configured explicitly.

## R2 · Carrying the negotiation

- **R2.1** When a peer sends a message naming a session it is in, the control plane shall deliver it to the other peer in that session and shall not deliver it to anybody else.
- **R2.2** The control plane shall name the sending peer in every message it delivers, taking that name from the connection and not from the message.
- **R2.3** If a message names a session the sending peer is not in, or no session at all, then the control plane shall refuse the message and shall tell the sender why.
- **R2.4** If the other peer in a session has no open channel, then the control plane shall tell the sender that the peer is not connected, rather than holding the message.
- **R2.5** The control plane shall carry the payload of a signaling message without reading it.
- **R2.6** If a message is larger than the configured maximum, then the control plane shall refuse it and shall close the connection that sent it.
- **R2.7** If a peer sends more messages than the configured allowance within the configured window, then the control plane shall refuse the excess and shall keep the connection open.
- **R2.8** If a peer does not accept a delivery within the configured send timeout, then the control plane shall close that peer's channel rather than make the sending peer wait.

## R3 · Keeping the channel honest

- **R3.1** The control plane shall keep a peer online in the registry for as long as its signaling channel is open, without that peer having to report in separately.
- **R3.2** If a peer stops answering the connection-level heartbeat within the configured timeout, then the control plane shall close its channel.
- **R3.3** When a session ends, the control plane shall tell both peers that hold a channel.
- **R3.4** The control plane shall report how many channels are open to the health endpoint.
