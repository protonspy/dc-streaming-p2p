# Glossary

One canonical term per concept, and the synonyms nobody should use for it.

- **central server** — the process that authenticates, registers, authorizes, and carries signaling; it never carries media. Avoid: signalling server, broker, hub
- **control plane** — everything the central server does: authentication, registry, authorization, session records, signaling
- **data plane** — the path audio and video actually travel, between two peers or across the relay
- **peer** — one endpoint of a call, identified by a peer identifier and holding at most one control-plane connection. Avoid: client node
- **peer identifier** — the durable identity of a peer, stable across reconnections and tokens. Avoid: peer name
- **token** — the short-lived credential proving a process is authorized to act as a peer identifier; it is not the identity
- **session** — the record of two peers connected or connecting to each other, identified by a session identifier and carrying its own state
- **registry** — the central server's record of which peers are online and when each was last seen
- **heartbeat** — the periodic message a peer sends to stay online in the registry; its absence moves the peer to suspect and then offline
- **signaling** — the exchange of session description and candidate messages that lets two peers negotiate a peer connection; it is not the media
- **peer connection** — the negotiated WebRTC connection between two peers, direct or relayed
- **relay** — the external TURN service that forwards media when no direct path exists. Avoid: relay node, relay server
- **ICE configuration** — the STUN and TURN servers and the ephemeral credentials handed to a peer before it negotiates
- **connection type** — whether an established peer connection is direct or relayed; observability, never application logic
