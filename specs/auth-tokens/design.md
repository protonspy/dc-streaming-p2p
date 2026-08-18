---
autonomy: auto
ci: wait
---

# Auth tokens — design

## What changes

Serves R1.1 through R4.4.

A new package `internal/auth` holding four things that have nothing to do with HTTP:
the client store, the token issuer, the token verifier, and the attempt limiter. The
HTTP surface stays in `internal/transport`, which gains `POST /auth`, the public-key
endpoint, and the middleware that puts a verified peer identifier into the request
context for every later handler to read.

`internal/config` gains the signing key, the client list, the token lifetime, and the
limiter's allowance and window. It already refuses to start on a bad value, and these
join that.

## Boundaries and contracts

**`POST /auth`** takes `{"client_id": …, "client_secret": …}` and answers
`{"token": …, "peer_id": …, "expires_at": …}`.

**`GET /.well-known/jwks.json`** answers a JSON Web Key Set with one key: the
standard shape a browser or another service already knows how to read, cacheable
because it changes only on rotation. Publishing a *set* rather than a single key is
what makes rotation possible without a new endpoint — during a rotation both keys
are listed.

One shape for every rejection, so a caller cannot learn from the difference:

```json
{ "error": "invalid_client" }
```

`invalid_client` for anything wrong with the credentials, `invalid_request` for a
body that could not be read, `too_many_attempts` when the limiter refused it, and
`invalid_token` or `token_expired` on the verification side — R2.3 requires those
two to be distinguishable, since one means retry with the same credentials and the
other means the peer was never allowed.

## Data

The token is a JWT signed with Ed25519 —
`adr:0006-sign-tokens-with-ed25519-and-publish-the-public-key` for why asymmetric —
carrying only what the control plane reads:

| Claim | What it is |
|---|---|
| `sub` | the peer identifier this token acts as |
| `iss` | the central server that issued it |
| `exp`, `iat` | the lifetime, bounded by the configured token lifetime |
| `jti` | a random identifier, so one token can be named in a log without quoting it |

Nothing else. A claim the server does not read is a claim someone will start
trusting.

Verification refuses any algorithm other than Ed25519 *before* checking the
signature. The parser is given a key function that ignores what the token asks for
and returns the one public key this server has, which is what keeps a token naming a
symmetric algorithm from being verified against the published key as if that key
were a shared secret.

A client is an identifier, the peer identifier it authenticates as, and a secret.
They are configured, not stored — this server has no database, and
`adr:0004-keep-the-registry-and-sessions-in-process-memory` is why. Secrets are held
as SHA-256 hashes and compared with `subtle.ConstantTimeCompare`. That is
deliberately not a password hash: these are machine credentials required to be at
least 32 characters, and the brute-force resistance comes from their length rather
than from the cost of the hash. Allowing short secrets would need bcrypt or argon2
instead, and refusing them at startup is what keeps that unnecessary. An unknown
client identifier is compared against a fixed dummy hash, so the absent client and
the wrong secret take the same path and the same time.

Failures are counted per client identifier in a fixed window, in memory. Past the
allowance the identifier is refused until the window passes, correct credentials
included — otherwise the limit is a hint about which secret is close. Counting per
identifier rather than per address is the choice that matters: peers arrive from
anywhere, and the thing being guessed is the secret for a named client. It also
means someone who knows a client identifier can lock it out, which is accepted here
because the alternative is unlimited guessing.

## Alternatives considered

**A symmetric signature** was the reading `adr:0005-issue-our-own-short-lived-tokens`
left open, and is rejected in `adr:0006-sign-tokens-with-ed25519-and-publish-the-public-key`:
every node that verifies could also forge.

**Hand-rolling the JWT** over `crypto/ed25519` is perhaps sixty lines and avoids a
dependency. Rejected: the parsing side is where token implementations go wrong, and
the failure is silent — a signature verified against the wrong key, or an algorithm
accepted that should not have been. A library that has been attacked in public is
worth more than sixty lines saved.

## Risks

The limiter's state is per process, so a restart forgives every attempt so far, and
a second instance would not share the count. That is the same single-node assumption
as the registry and it degrades the same way; it is called out here because a rate
limit that quietly resets is worth less than it appears.
