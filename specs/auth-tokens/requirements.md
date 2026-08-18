---
autonomy: auto
ci: wait
---

# Auth tokens — requirements

## Purpose

Every peer has to prove which peer it is before it can register, be discovered, or
signal. This feature is the whole of that proof: a client identifier and a secret
are exchanged for a short-lived token, and the token is what every later
control-plane request carries. It is for the browser SDK, which authenticates once
and reconnects under a fresh token when the old one runs out.

The peer identifier is the durable identity and the token is not; see
`adr:0005-issue-our-own-short-lived-tokens`.

## R1 · Issuing a token

- **R1.1** The authentication service shall accept a client identifier and a client secret at `POST /auth`.
- **R1.2** When a request presents credentials matching a configured client, the authentication service shall return a token naming that client's peer identifier as its subject, the moment it expires, and the number of seconds until then.
- **R1.3** The authentication service shall issue tokens that expire within the configured token lifetime.
- **R1.4** If the credentials match no configured client, then the authentication service shall refuse the request without disclosing which half of the credentials was wrong.
- **R1.5** If the request body is not a credential pair the service can read, then the authentication service shall refuse it as a bad request.
- **R1.6** If a client identifier fails to authenticate more times than the configured allowance within the configured window, then the authentication service shall refuse further attempts for that identifier until the window passes, whether or not the credentials later presented are correct.
- **R1.7** The authentication service shall take the same observable time to reject an unknown client identifier as to reject a known one presenting the wrong secret.

## R2 · Verifying a token

- **R2.1** The control plane shall serve an authenticated request only when it carries a token this server signed and whose expiry has not passed.
- **R2.2** If a request carries no token, a token this server did not sign, or a token whose expiry has passed, then the control plane shall refuse it as unauthorized.
- **R2.3** When a token is refused because it expired, the control plane shall say so distinctly enough that a peer can tell it apart from a token that was never valid.
- **R2.4** When a request is served, the control plane shall act on it as the peer identifier the token names and shall not read that identifier from anywhere else in the request.
- **R2.5** The control plane shall keep tokens and client secrets out of its logs, its error responses, and its health endpoint.

## R3 · Configured clients

- **R3.1** The authentication service shall read its clients from configuration at startup, each client carrying an identifier, a peer identifier, and the secret it authenticates with.
- **R3.2** The authentication service shall hold each configured secret only as a hash and shall compare a presented secret in a way that does not reveal, by how long it takes, how much of the secret was right.
- **R3.3** If a configured secret is shorter than the minimum this service accepts, then the server shall refuse to start and shall name the client it refused.
- **R3.4** If no clients are configured, then the server shall refuse to start.
