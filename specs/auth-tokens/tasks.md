---
autonomy: auto
ci: wait
---

# Auth tokens — tasks

## 1 · The auth package

- [ ] 1.1 (Unit) Add the client store: configured clients, hashed secrets, constant-time comparison, and a fixed-cost path for an unknown identifier — R4.1, R4.2
- [ ] 1.2 (Unit) Validate the client configuration at startup and refuse to start on a missing field, a short secret, or an empty list — R4.3, R4.4
  _Depends 1.1_
- [ ] 1.3 (TDD) Issue Ed25519-signed tokens carrying the peer identifier, the issuer, and a bounded lifetime — R1.2, R1.3
  _Depends 1.1_
- [ ] 1.4 (TDD) Verify a token: signature, expiry, and refusal of any algorithm other than the one issued — R2.1, R2.2, R2.3, R2.5
  _Depends 1.3_
- [ ] 1.5 (TDD) Refuse a client identifier that fails more often than the allowance within the window, correct credentials included — R1.6
  _Depends 1.1_

## 2 · The HTTP surface

- [ ] 2.1 (Unit) Serve POST /auth: one rejection shape for every credential failure, and the same observable cost for an unknown client as for a wrong secret — R1.1, R1.4, R1.5, R1.7
  _Depends 1.2, 1.3, 1.5_
- [ ] 2.2 (Unit) Publish the public key as a JWK set, unauthenticated, with the signing key held back — R3.1, R3.2
  _Depends 1.3_
- [ ] 2.3 (Unit) Authenticate a request from its token and carry the peer identifier in its context, taking it from nowhere else — R2.4
  _Depends 1.4_
- [ ] 2.4 (Unit) Keep tokens and secrets out of logs, error bodies, and the health endpoint — R2.6
  _Depends 2.1, 2.3_

## 3 · The client side

- [ ] 3.1 (Unit) Pin the expected public key in the client: fetch the key set and refuse a server presenting any other key before sending a credential — R3.3
  _Depends 2.2_
