---
autonomy: auto
ci: wait
---

# Session lifecycle — design

## What changes

Serves R1.1 through R4.2.

A new package `internal/session`: the records, the state machine, and the
authorization decision. `internal/transport` gains the routes that open a session,
read one, report what happened to it, and close it. The health endpoint's session
counter, wired to zero since the foundation, is connected here.

## Boundaries and contracts

| Route | What it does |
|---|---|
| `POST /sessions` | opens a session to the peer named in the body |
| `GET /sessions/{id}` | reports a session to a peer in it |
| `POST /sessions/{id}/state` | reports connected, failed, or closed |
| `DELETE /sessions/{id}` | closes it |

The body of `POST /sessions` names the peer being *called*, which is the one
identifier that legitimately comes from a request: the caller is the token, the
callee is what the caller asked for.

A caller that is not in a session is answered `404`, the same as one asking about a
session that never existed — a session identifier is not a capability, and the
difference would let a peer discover who is talking to whom.

## Data

```
session id → { caller, callee, state, path, opened at, changed at }
```

State is stored here, unlike the registry's, because it does not follow from a
clock: it follows from what a peer reported. What *is* derived is the failure at the
negotiation timeout — a session still negotiating past it is failed whether or not
anything swept it yet, exactly as an expired peer is gone whether or not the sweeper
has run.

```
negotiating ──reports connected──▶ connected ──close──▶ closed
     │                                  │
     ├──reports failed──▶ failed ◀──────┘ (the timeout reaches only negotiating)
     └──close──▶ closed
```

Anything else is refused and the session is left as it was — R4.2. A connected
session cannot become negotiating again: a renegotiation is the same session
carrying on, and the SDK reports the path it ended up on rather than the states it
passed through.

The path is `direct` or `relayed`, reported by the peer because only the peer knows
which candidate pair won. It is observability: nothing in the control plane behaves
differently for one or the other, and a peer that lies about it changes nothing
except a number on a dashboard.

## Authorization

One decision, in one place: an authorizer that answers whether a caller may reach a
callee. The implementation this spec ships allows any authenticated peer to reach
any registered peer, which is what the first deployments need and what the demo
requires.

It is an interface rather than a condition inside a handler because the honest
alternative is worse: a policy scattered across handlers is one somebody will forget
to apply. Contact lists, rooms, or an external decision all replace this one
implementation without touching a route. R1.4 is written so that a refusal by policy
is indistinguishable from a peer that does not exist, which is the property that
survives whatever policy comes later.

## Risks

Two peers opening a session to each other at the same instant would make two
sessions, and R1.7 makes that one: the pair is looked up under a key that does not
depend on which of them asked first. That lookup and the insert happen under one
lock, so the race resolves rather than duplicating.

A session outlives the peers in it if nothing removes it — which is why R2.6 exists,
and why the registry's expiry notifies the session store rather than the session
store polling the registry.
