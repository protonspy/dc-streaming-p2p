---
autonomy: auto
ci: wait
---

# Signaling channel — tasks

## 1 · The hub

- [x] 1.1 (Unit) Add the hub: one connection per peer, a second closing the first, and deregistering on close — R1.1, R1.3, R1.5
- [x] 1.2 (TDD) Deliver a message to the other peer in the session and to nobody else, naming the sender from the connection — R2.1, R2.2
  _Depends 1.1_
- [x] 1.3 (Unit) Refuse a message naming a session the sender is not in, and one naming no session — R2.3
  _Depends 1.2_
- [x] 1.4 (Unit) Tell a sender that the other peer holds no channel rather than holding the message — R2.4
  _Depends 1.2_
- [x] 1.5 (Unit) Carry the payload without reading it — R2.5
  _Depends 1.2_
- [x] 1.6 (Unit) Refuse a message past the allowance in the window, keeping the connection, and close the connection for one past the size limit — R2.6, R2.7
  _Depends 1.1_
- [x] 1.7 (Unit) Count the open channels — R3.4
  _Depends 1.1_

## 2 · The connection

- [x] 2.1 (Unit) Upgrade GET /signal, taking the token from the subprotocol and refusing a connection without a valid one — R1.1, R1.2
  _Depends 1.1_
- [x] 2.2 (Unit) Refuse an origin that is not among the configured ones — R1.4
  _Depends 2.1_
- [x] 2.6 (Unit) Refuse to start with no origins configured, and accept every origin only where that was asked for — R1.6
  _Depends 2.2_
  _Reason The security review found an unset value quietly allowing every origin_
- [x] 2.3 (Unit) Keep a connected peer online in the registry, and close a channel that stops answering the heartbeat — R3.1, R3.2
  _Depends 2.1_
- [x] 2.4 (Unit) Bound every write with a deadline, so a peer that stops reading cannot hold up the peer writing to it — R2.8
  _Depends 2.1_
- [x] 2.5 (Unit) Tell both peers when a session ends, and wire the channel count into health — R3.3, R3.4
  _Depends 2.1, 1.7_
