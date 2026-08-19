---
status: accepted
---

# Sign tokens with Ed25519 and publish the public key

## Context

`adr:0005-issue-our-own-short-lived-tokens` settled that this server issues its own
tokens. It left the signing open, and a symmetric signature was the obvious reading:
one secret, held by the issuer, used to sign and to verify.

That reading stops working as soon as anything other than the issuer needs to verify
a token. Peer servers and anchors do: a peer arrives at a node the central server
does not operate, presents a token, and that node has to decide whether to believe
it. With a symmetric key, believing a token means holding the key that mints them,
so every node that verifies can also forge, and the secret spreads to exactly the
machines least able to protect it.

## Decision

Tokens are signed with Ed25519. The central server holds the private key; the public
key is published, unauthenticated, and anything may verify with it.

Verification pins the algorithm: a token that names anything other than Ed25519 is
refused before its signature is checked. That is what closes the substitution attack
where a token arrives claiming to be signed with a symmetric algorithm keyed by the
public value everybody already has.

A client is configured with the central server's URL *and* the public key it expects
to find there, and refuses a server presenting any other key before it sends a
credential.

## Consequences

A node can verify a peer without being able to impersonate one, which is what makes
federation possible at all: an anchor operated by someone else verifies tokens and
mints nothing. The public key is not a secret, so publishing it costs nothing and
distributing it is a configuration problem rather than a key-management one.

Ed25519 is in the standard library, so this adds no dependency for the signing
itself, and its signatures are small and fast enough that verification per request
is not worth caching.

Rotation is now a real operation with a real failure mode: a client pinned to the
old key refuses the new one, so a rotation has to publish both for as long as the
longest-lived pinned configuration, and pinning is what makes that necessary. The
private key becomes the single most valuable thing the server holds — losing it
forges every peer, and there is still no revocation before expiry, which is why the
lifetime stays short.
