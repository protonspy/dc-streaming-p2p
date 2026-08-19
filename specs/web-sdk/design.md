---
autonomy: auto
ci: wait
---

# Web SDK — design

## What changes

Serves R1.1 through R3.3.

`web/sdk/` — plain ES modules, no build step and no dependencies. A browser loads
them directly, and so does `node --test`, which is what makes them testable at all.

The server gains one route, `GET /demo/`, serving that directory and a page that
opens two calls in two tabs. It is behind no token: the page is what *gets* a token.

## Boundaries and contracts

```js
const client = new PeerClient({
  serverURL: "https://central.example",
  clientID: "sdk-web",
  clientSecret: "…",
  publicKey: "…",          // optional; refuses any other, before authenticating
});

await client.start();                       // authenticate, register, connect
const call = await client.call("peer-002", { localStream });
call.on("track", (stream) => { … });        // the far side's media
call.on("state", ({ state, path }) => { … });
await call.close();
```

`state` is one of `connecting`, `connected`, `failed`, `closed` — what an
application acts on. Nothing carries a session description or a candidate outward:
those exist between `RTCPeerConnection` and the signaling channel and nowhere else.

## Data

The SDK holds one signaling channel and a call per session:

```
session id → { peerConnection, remoteStream, state, path }
```

The far side of a call is decided by who asked. The peer that called sends the
offer; the peer that was called answers the offer that arrives — which is R2.5, and
is why a call object exists before the application has been told about it.

The path is read from the selected candidate pair once the connection settles:
`relay` on either end means relayed, anything else is direct. It is reported to the
application and to the control plane, and nothing branches on it.

## What the browser is, and what a test replaces

Everything the SDK touches from outside is injected, defaulting to what a browser
has:

| What | Default | In a test |
|---|---|---|
| `fetch` | the browser's | a function answering the control plane's routes |
| `WebSocket` | the browser's | a fake pair of sockets connected to each other |
| `RTCPeerConnection` | the browser's | a fake that records what it was told and fires what a test wants |

That is the whole of the seam, and it is what lets the SDK's own logic — the order
of the calls, who offers, what happens when the channel drops — be tested without a
browser or a network. It is not a mock of WebRTC's semantics: the tests assert what
the SDK does, and what WebRTC does with a session description is not this codebase's
to verify.

## Reconnecting

The channel is reopened with a delay that doubles, capped, with jitter, until the
application closes the client. Calls in progress are *not* torn down when the
channel drops: an established peer connection does not need the server, and
`adr:0001-split-control-plane-from-data-plane` is why. A call still negotiating when
the channel drops fails, because it cannot finish.

Jitter matters more than the backoff: a server restarting has every peer reconnect
at once, and identical timers turn one restart into a second outage.

## Risks

A token expiring mid-call is handled by obtaining another, but the signaling channel
carries the token it opened with — the server does not re-check it, so a channel
outlives its token by design. That is a deliberate trade: re-authenticating a live
socket means dropping and reopening it, which costs a real call to close a window
that requires stealing a token to exploit.
