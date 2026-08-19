---
autonomy: auto
ci: wait
---

# ICE configuration — requirements

## Purpose

Before two peers can try to reach each other they need to know where to look: the
STUN servers that tell a peer how it appears from outside, and the relay to fall
back to when no direct path exists. This feature hands a peer that list, with
credentials for the relay that are useless to anyone who reads them an hour later.

The relay is external and the credentials are signed rather than stored —
`adr:0003-use-an-external-turn-service-with-ephemeral-credentials`.

## R1 · Handing out the configuration

- **R1.1** The control plane shall answer an authenticated peer with the STUN servers and, where a relay is configured, the relay servers it should use.
- **R1.2** Where a relay is configured, the control plane shall issue credentials for it that expire within the configured credential lifetime.
- **R1.3** The control plane shall build a relay credential as an expiry timestamp joined to the peer identifier, and a password that is the signature of that username under the shared secret.
- **R1.4** The control plane shall keep the shared secret out of every response, log and error.
- **R1.5** If no relay is configured, then the control plane shall answer with the STUN servers alone and shall say that no relay is available.
- **R1.6** The control plane shall serve the configuration only to an authenticated caller, and shall name that caller's peer identifier in the credential it issues.

## R2 · Answering in the shape a browser reads

- **R2.1** The control plane shall answer in the shape a browser accepts as its ICE server list, so that a peer passes it on without rewriting it.
- **R2.2** The control plane shall tell the peer when the credential expires, so that it can ask for another before starting a call that would outlive it.
