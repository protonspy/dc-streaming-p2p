---
autonomy: auto
ci: wait
---

# Session lifecycle — tasks

## 1 · The store

- [x] 1.1 (Unit) Add the session store: open a session, read one, and hold at most the configured number — R1.1, R1.6, R4.1
- [x] 1.2 (TDD) Move a session between states, refusing a move it cannot make and leaving it as it was — R2.1, R2.2, R2.3, R2.4, R4.2
  _Depends 1.1_
- [x] 1.3 (Unit) Answer an existing session for a pair that already has one, whichever of them asks — R1.7
  _Depends 1.1_
- [x] 1.4 (TDD) Fail a session still negotiating past the timeout, and forget an ended one past its retention — R2.5, R2.7
  _Depends 1.2_
- [x] 1.5 (Unit) Close every session a peer was in when that peer leaves the registry — R2.6
  _Depends 1.2_
- [x] 1.6 (Unit) Decide whether a caller may reach a callee, behind one interface — R1.4
  _Depends 1.1_
- [x] 1.7 (Unit) Count live sessions for the health endpoint — R3.3

## 2 · The HTTP surface

- [x] 2.1 (Unit) Open a session: the caller from the token, the callee from the request, refusing an unreachable peer, one it may not reach, and itself — R1.1, R1.2, R1.3, R1.5, R3.4
  _Depends 1.1, 1.3, 1.6_
- [x] 2.2 (Unit) Report a session to a peer in it, and answer as though it did not exist to anyone else — R3.1, R3.2
  _Depends 1.1_
- [x] 2.3 (Unit) Take the report of what happened — connected with its path, failed, or closed — R2.2, R2.3, R2.4
  _Depends 1.2_
- [x] 2.4 (Unit) Wire the counts into health, the timeout sweep into the server's lifetime, and the registry's expiry into closing sessions — R2.5, R2.6, R3.3
  _Depends 1.4, 1.5, 1.7_

## 3 · What the review found

- [x] 3.1 (Unit) Keep the live count and the per-peer count rather than scanning, so an unauthenticated health check cannot make everybody wait on the lock — R3.3, R4.1
  _Reason The security review traced a full scan under the mutex on every open and every health check_
- [x] 3.2 (Unit) Cap the live sessions one peer may hold, and answer that caller distinctly from the deployment being full — R4.3
  _Reason The security review found one caller able to take a slot against every peer it can reach_
- [x] 3.3 (Unit) Bound the retained records and drop the oldest ended first — R4.4
  _Reason The security review found open-and-close in a loop growing the map for a whole retention window_
