---
autonomy: auto
ci: wait
---

# Session lifecycle — requirements

## Purpose

A session is the record that two peers are trying to reach each other, or have.
It is what authorization is decided on, what signaling is routed through, and what
says whether a call ended because it was hung up or because it never connected.

It holds no media and no session description: those live between the peers. What it
holds is who, since when, in what state, and over what path.

## R1 · Opening a session

- **R1.1** When an authenticated peer asks to reach another peer, the control plane shall create a session naming the caller and the peer it asked for.
- **R1.2** The control plane shall take the calling peer identifier from the verified token and shall not take it from the request.
- **R1.3** If the peer asked for is not in the registry, then the control plane shall refuse the session and shall say that the peer is not reachable.
- **R1.4** If the caller is not allowed to reach the peer it asked for, then the control plane shall refuse the session without saying whether that peer exists.
- **R1.5** If a peer asks to reach itself, then the control plane shall refuse the session.
- **R1.6** When a session is created, the control plane shall answer with its identifier and the state it is in.
- **R1.7** While a session between two peers is open, the control plane shall answer a second request between the same two peers with the session that already exists rather than a second one.

## R2 · The life of a session

- **R2.1** The control plane shall record a session as negotiating until a peer in it reports otherwise.
- **R2.2** When a peer in a session reports that it connected, the control plane shall record the session as connected and shall record whether the path is direct or relayed.
- **R2.3** When a peer in a session reports that it failed, the control plane shall record the session as failed.
- **R2.4** When a peer in a session closes it, the control plane shall record the session as closed.
- **R2.5** If a session is still negotiating after the configured negotiation timeout, then the control plane shall record it as failed.
- **R2.6** If a peer in a session leaves the registry, then the control plane shall close every session that peer was in.
- **R2.7** The control plane shall keep a session that has ended only as long as the configured retention, and shall then forget it.

## R3 · Reading and reporting

- **R3.1** The control plane shall answer, to a peer in a session, that session's state, its participants, its path, and when it changed.
- **R3.2** If a peer that is not in a session asks about it, then the control plane shall answer as though the session did not exist.
- **R3.3** The control plane shall report how many sessions are live to the health endpoint.
- **R3.4** The control plane shall serve every session route only to an authenticated caller.

## R4 · Bounds

- **R4.1** The control plane shall hold at most the configured number of sessions, and shall refuse a new one rather than dropping one that is connected.
- **R4.2** The control plane shall refuse a state a session cannot move to, and shall leave the session as it was.
