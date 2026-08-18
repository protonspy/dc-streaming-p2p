---
status: accepted
---

# Split the control plane from the data plane

## Context

Two browsers need to exchange audio and video. A server can carry that media itself,
forwarding every stream it receives, or it can restrict itself to helping the two
sides find each other and then step out of the path. The first costs bandwidth per
call and grows with every participant; the second costs a connection per peer and
stays flat.

The media path is also the part with the hardest failure mode: a peer behind CGNAT or
a corporate firewall may have no direct path at all, and something has to carry those
calls regardless.

## Decision

The central server owns the control plane only — authentication, the registry of who
is online, authorization, session records, and the signaling messages two peers need
to negotiate. Media is a WebRTC peer connection between the two browsers. When the
direct path fails, ICE falls back to a relay that is not this server.

The peer connection is the abstraction the application sees. Whether a call is direct
or relayed is infrastructure, reported for observability and never branched on by
application code.

## Consequences

The server's cost per call is a WebSocket and a session record, not a media stream. It
can be restarted without dropping a call in progress, because an established peer
connection does not depend on it.

The cost is that failures move to where they are hardest to observe: a call that never
connects fails inside ICE on the client, not in a server log. The SDK has to report
connection state and the selected candidate pair back through the control plane, or
the server knows a session exists and nothing about whether it works.

Media cannot be recorded, transcoded, or moderated on the server under this decision.
A requirement for any of those reopens it.
