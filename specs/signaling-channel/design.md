---
autonomy: auto
ci: wait
---

# Signaling channel — design

## What changes

Serves R1.1 through R3.4.

A new package `internal/signaling`: the hub that knows which peer holds which
connection, and the envelope those connections exchange. `internal/transport` gains
`GET /signal`, which upgrades and hands the connection to the hub.

The hub depends on the session store to answer one question — *is this peer in that
session, and who is the other side* — and on the registry to keep a connected peer
online. It reads no payload.

## Boundaries and contracts

The envelope is fixed; the payload is not read:

```json
{ "type": "offer", "session_id": "…", "payload": { } }
```

`type` is one of `offer`, `answer`, `candidate`, `bye`, which is the whole of what
two browsers need to negotiate. `payload` is carried through untouched — a server
that parses a session description is a server that has to be updated when the
browsers change one.

What the server sends back uses the same envelope with `from` filled in, and adds
two kinds of its own:

```json
{ "type": "offer", "session_id": "…", "from": "peer-001", "payload": { } }
{ "type": "error", "session_id": "…", "code": "peer_not_connected" }
{ "type": "session", "session_id": "…", "state": "closed" }
```

`from` is written by the server from the connection's identity. A `from` in an
inbound message is ignored rather than refused, because the SDK has no reason to set
it and refusing would be one more thing to get wrong on a path that already knows
who is talking.

**The token travels in the subprotocol.** A browser cannot set headers on a
WebSocket, which leaves the query string or `Sec-WebSocket-Protocol`. A token in a
query string lands in access logs, proxy logs, and browser history; the subprotocol
does not. The client offers `["dc-signal.v1", "dc-token." + token]` and the server
selects `dc-signal.v1`, which is the same shape everything from Kubernetes to
managed WebSocket providers uses for this.

## Data

The hub holds one connection per peer:

```
peer id → connection
```

A second connection for a peer closes the first — R1.3. The alternative, refusing
the new one, strands a peer whose old connection is half-dead: the peer cannot tell
the difference between "my connection is fine" and "the server thinks it is", and
the reconnect is exactly what a peer does when it doubts.

Delivery is a write to the other peer's connection, under that connection's own
write lock: one connection is written to by the goroutine reading its own socket and
by whichever goroutine is delivering to it, and a WebSocket connection cannot have
two writers at once.

Nothing is queued. A peer with no channel is reported to the sender, not held for
later — a signaling message is only meaningful while both sides are negotiating, and
a queue here would deliver a stale offer to a peer that has moved on.

## Backpressure and abuse

Every write has a deadline. A peer that stops reading its socket must not be able to
block the peer writing to it, so a delivery that cannot complete inside the deadline
closes the slow connection rather than making the sender wait.

Messages are counted per connection in a window: past the allowance the message is
refused and the connection stays open, because a peer that is merely too eager
should slow down rather than reconnect and try again. A message past the size limit
is different — the connection is closed, since a peer sending more than the limit is
not doing signaling.

## Risks

The session store and the hub can disagree for an instant: a session closes while a
message for it is in flight, and the message is delivered to a peer that has just
been told the session ended. The receiving SDK drops a message for a session it has
closed, which is the same thing it must already do for a message that arrives during
its own teardown.

Keeping a peer online for as long as its channel is open (R3.1) means the registry's
heartbeat becomes redundant for connected peers, and the two mechanisms can disagree
about a peer whose channel is open but whose process is wedged. The connection-level
heartbeat is what settles it: a peer that does not answer a ping loses its channel,
and then the registry's own window takes over.
