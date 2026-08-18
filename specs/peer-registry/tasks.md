---
autonomy: auto
ci: wait
---

# Peer registry — tasks

## 1 · The store

- [ ] 1.1 (Unit) Add the peer store: register, replace an existing registration, deregister, and report what is held — R1.1, R1.3, R1.5, R3.1, R3.2
- [ ] 1.2 (TDD) Derive a peer's state from how long it has been silent, across the online, suspect and gone boundaries — R2.2, R2.3, R2.4
  _Depends 1.1_
- [ ] 1.3 (Unit) Record a report from a registered peer, and refuse one from a peer that was removed — R2.1, R2.5
  _Depends 1.2_
- [ ] 1.4 (Unit) Refuse a registration past the configured ceiling rather than evicting an online peer — R4.1
  _Depends 1.1_
- [ ] 1.5 (Unit) Expire records on a ticker, without waiting for anything to read them — R4.2
  _Depends 1.2_
- [ ] 1.6 (Unit) Count what is held, by state, for the health endpoint — R3.3
  _Depends 1.2_

## 2 · The HTTP surface

- [ ] 2.1 (Unit) Serve register and deregister behind the token, taking the peer identifier from it and never from the body — R1.1, R1.2, R1.4, R1.5, R3.4
  _Depends 1.1, 1.3_
- [ ] 2.2 (Unit) Serve the heartbeat, answering a removed peer distinctly enough that it knows to register again — R2.1, R2.5
  _Depends 1.3_
- [ ] 2.3 (Unit) Serve the lookup, answering the same shape for a peer that never existed and one that has gone — R3.1, R3.2
  _Depends 1.1_
- [ ] 2.4 (Unit) Wire the registry's counts into the health endpoint and its ticker into the server's lifetime — R3.3, R4.2
  _Depends 1.5, 1.6_
