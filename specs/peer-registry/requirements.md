---
autonomy: auto
ci: wait
---

# Peer registry — requirements

## Purpose

The registry is what the control plane knows about who is reachable right now: a
peer registers when it comes up, says so periodically, and disappears when it stops.
Everything downstream reads from it — discovery asks it who exists, sessions ask it
whether the other side is still there.

It holds no history and survives nothing: a peer that reconnects registers again,
and the record is disposable by design —
`adr:0004-keep-the-registry-and-sessions-in-process-memory`.

## R1 · Registering

- **R1.1** When an authenticated peer registers, the registry shall record it as online under the peer identifier its token names.
- **R1.2** The registry shall take the peer identifier from the verified token and shall not take it from the request body.
- **R1.3** When a peer registers while already registered, the registry shall replace what it held rather than refusing the registration.
- **R1.4** When a peer registers, the registry shall answer with the interval at which it is expected to report in.
- **R1.5** When a peer deregisters, the registry shall remove it, and shall report nothing about a peer identifier it does not hold.

## R2 · Staying online

- **R2.1** When a registered peer reports in, the registry shall record the moment and keep it online.
- **R2.2** While a peer has been silent for longer than the suspect window, the registry shall record it as suspect and shall keep answering for it.
- **R2.3** While a peer has been silent for longer than the offline window, the registry shall remove it.
- **R2.4** If a peer reports in while suspect, then the registry shall return it to online.
- **R2.5** If a peer reports in after it was removed, then the registry shall refuse the report and shall say that registration is required.

## R3 · Reading the registry

- **R3.1** The registry shall answer, for a peer identifier it holds, that peer's state and when it was last seen.
- **R3.2** If a peer identifier is not held, then the registry shall answer that it is not there, in the same shape whether it was never registered or has since been removed.
- **R3.3** The registry shall report how many peers it holds, in each state, to the health endpoint.
- **R3.4** The registry shall serve every read and write only to an authenticated caller.

## R4 · Bounds

- **R4.1** The registry shall hold at most the configured number of peers, and shall refuse a registration that would exceed it rather than evicting one that is online.
- **R4.2** The registry shall remove peers past the offline window without waiting to be asked about them.
