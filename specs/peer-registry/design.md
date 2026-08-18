---
autonomy: auto
ci: wait
---

# Peer registry — design

## What changes

Serves R1.1 through R4.2.

A new package `internal/registry` holding the peer records and the state machine
over them, and nothing else: no HTTP, no authentication, no timers of its own beyond
the one that expires records.

`internal/transport` gains four handlers behind `RequireToken` — register,
deregister, heartbeat, and lookup — which is what finally mounts the middleware
built in `specs/auth-tokens/`. The health endpoint's peer counter, wired to zero
since the foundation, is connected here.

## Boundaries and contracts

| Route | What it does |
|---|---|
| `POST /peers` | registers the caller; answers the heartbeat interval |
| `DELETE /peers` | deregisters the caller |
| `POST /peers/heartbeat` | keeps the caller online |
| `GET /peers/{id}` | reports one peer's state and when it was last seen |

Every one of them takes the acting peer identifier from the verified token. The
route carries an identifier only where a peer is being *read*, never where one is
being written: a peer can register itself and nothing else, which is R1.2 and is the
reason there is no `POST /peers/{id}`.

`GET /peers/{id}` answers `404` with the same body whether the peer was never
registered or has been removed. Distinguishing them would let a caller learn who
used to be here, and there is no reason a peer needs to know.

A heartbeat from a peer that was removed answers `409` with `registration_required`,
which is distinct from `404` on purpose: the peer is being told what to do about it,
and it is the one case where a caller learns something about its own record.

## Data

```
peer identifier → { state, registered at, last seen }
```

State is derived, never stored: a record's state is a function of how long it has
been since it was last seen, compared against the configured suspect and offline
windows. Storing it would mean two records of one fact, and a stored state goes
stale between the moment it changes and the moment something notices.

Online → suspect → gone, and any report in returns it to online. Suspect is not a
refusal: the registry still answers for a suspect peer, because a peer that missed
one heartbeat is far more likely to be there than not, and the caller that tries it
will find out faster than the registry can.

Expiry runs on a ticker in the process rather than only when a record is read, so a
peer that nobody asks about still leaves — R4.2. A read that finds an expired record
treats it as gone regardless, so the ticker's interval bounds the memory rather than
the correctness.

## Alternatives considered

**Expiring only on read** is simpler and needs no goroutine, and it was rejected
because the registry's whole purpose is answering *who is here*: a record nobody
reads is exactly the one that should not be counted, and the health endpoint reports
counts nobody read.

**Storing the state and updating it on a schedule** was rejected for the reason
above: two records of one fact, and the copy is the one that goes stale.

## Risks

The ticker and a request can touch the same record, so every path through the store
takes the lock; the tests run the concurrent case rather than reasoning about it.

The bound in R4.1 refuses a new registration when the registry is full, which means
a flood of registrations from valid clients can keep a legitimate peer out. That is
deliberate: evicting an online peer to admit a new one would drop a working call, and
the failure that matters more is the silent one.
