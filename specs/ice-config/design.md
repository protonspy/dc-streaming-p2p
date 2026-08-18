---
autonomy: auto
ci: wait
---

# ICE configuration — design

## What changes

Serves R1.1 through R2.2.

A new package `internal/ice` that turns the configured STUN and TURN URLs, plus the
shared secret, into the list a browser can use — and nothing else. `internal/transport`
gains `GET /ice-config` behind `RequireToken`.

## Boundaries and contracts

```json
{
  "ice_servers": [
    { "urls": ["stun:stun.example:3478"] },
    { "urls": ["turn:relay.example:3478"], "username": "1755561600:peer-001", "credential": "…" }
  ],
  "expires_at": "2026-08-19T00:00:00Z",
  "relay_available": true
}
```

`ice_servers` is exactly what `RTCPeerConnection` takes, so the SDK passes it through
without knowing what a relay is. `relay_available` is reported rather than inferred
from an empty list: a peer that cannot find a direct path and has no relay should say
so, not fail as though the network were at fault.

## Data

The credential is the mechanism coturn and every managed provider implement:

```
username   = <unix expiry>:<peer identifier>
credential = base64( HMAC-SHA1( shared secret, username ) )
```

HMAC-SHA1 is not a choice this project gets to make — the relay verifies exactly
this construction, and the security of the scheme rests on the secret and the short
lifetime rather than on the hash. The secret never leaves the server; what a browser
receives is one signature, valid for minutes.

The peer identifier goes in the username because that is what makes a relayed
allocation attributable: the relay logs it, so an allocation can be traced to a peer
without the relay knowing anything else about it.

## Risks

The credential outlives the answer that carried it, by exactly the configured
lifetime. A call that starts near the end of that window can outlast its own
credential, which is why `expires_at` is answered — the SDK is expected to ask for
another rather than discover the relay refusing mid-call.
