---
status: accepted
---

# Use an external TURN service with ephemeral credentials

## Context

Some fraction of calls cannot establish a direct path and need a relay. Running that
relay means operating a service that terminates untrusted traffic, amplifies bandwidth
if misconfigured, and is attractive to abuse as an open proxy.

Static TURN credentials shipped to a browser are public the moment the page loads.

## Decision

TURN is an external service — coturn in development, a managed provider or an operated
deployment in production — and this repository does not implement one. The central
server holds the shared secret and issues time-limited credentials with the standard
REST mechanism: the username is an expiry timestamp, the password is its HMAC-SHA1
under the shared secret. Credentials are handed to a peer as part of its ICE
configuration and expire on their own.

## Consequences

No relay code, no relay operations, and a credential that is useless to whoever
scrapes it an hour later. The shared secret never leaves the server.

The relay becomes a third-party dependency in the media path, with a bill proportional
to relayed minutes and its own availability. The server has no visibility into whether
it is working — a peer that cannot reach the relay looks the same as a peer that
failed ICE for any other reason.

Media stays encrypted end to end across the relay: DTLS-SRTP terminates at the peers,
so the relay forwards ciphertext it cannot read. That property is what makes an
untrusted third-party relay acceptable at all.
