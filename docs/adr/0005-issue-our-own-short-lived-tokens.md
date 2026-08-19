---
status: accepted
---

# Issue our own short-lived tokens

## Context

Every peer has to prove which peer it is before it can register, be discovered, or
signal. That identity could come from an external provider over OIDC, or the server
could issue its own credential from a client identifier and a secret.

The first peers are browser clients of an application we control, and there is no
existing identity provider in this project to integrate with.

## Decision

`POST /auth` takes a client identifier and a secret and returns a signed JWT that
expires within the hour, carrying the peer identifier as its subject. The token
authorizes the control-plane connection; the peer identifier is the durable identity
and the token is not.

Verification is a function over the token, so an OIDC verifier can be added beside
this one without changing the callers.

## Consequences

No external dependency and no integration to stand up. The three concepts stay
separate — peer identifier, token, session identifier — which is what lets a peer
reconnect under a new token and still be the same peer.

We now own credential storage, secret rotation, and the signing key. A leaked signing
key forges any peer, and there is no revocation before expiry: a stolen token is valid
until it runs out. Shortening the lifetime is the only lever, and it trades against
reconnection churn.
