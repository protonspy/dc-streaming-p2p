---
autonomy: auto
ci: wait
---

# ICE configuration — tasks

- [x] 1.1 (TDD) Build a relay credential: the expiry-and-peer username, and its signature under the shared secret — R1.2, R1.3
- [x] 1.2 (Unit) Assemble the server list from the configured STUN and relay URLs, saying whether a relay is available — R1.1, R1.5
  _Depends 1.1_
- [x] 1.3 (Unit) Serve GET /ice-config behind the token, naming the calling peer in the credential and reporting when it expires — R1.6, R2.1, R2.2
  _Depends 1.2_
- [x] 1.4 (Unit) Keep the shared secret out of the response, the logs and the errors — R1.4
  _Depends 1.3_
