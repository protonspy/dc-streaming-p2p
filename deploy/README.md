# Development deployment

```bash
docker compose -f deploy/compose.yaml up --build
curl -s localhost:8080/healthz
```

`relay_configured` in that response says whether the server has a relay to offer.
`false` means a peer with no direct path has nowhere to go, and that is a
configuration problem rather than a server that is down.

## What this proves and what it does not

The compose file runs coturn on the host network so it can advertise addresses a
browser will actually try. On Linux that is close to a real deployment. On Docker
Desktop, host networking is emulated and the relay may advertise an address the
browser cannot reach — a relayed call that fails there has not necessarily failed
in the field, and one that succeeds has not proven it works behind a real NAT.

The signing key is an Ed25519 seed in base64. Generate a real one with:

```bash
openssl rand -base64 32
```

The public half is served at `/.well-known/jwks.json`, and a client that pins the
key compares what it gets there against the copy it was configured with. Clients
themselves come from `CENTRAL_CLIENTS` as JSON, or from a file named by
`CENTRAL_CLIENTS_FILE` — the file is the better one for anything real, because an
environment variable is readable by anything that can list the process.

Every secret in these files is a development value, committed on purpose so the
stack starts with no setup. A deployment takes `CENTRAL_TOKEN_SIGNING_KEY` and
`CENTRAL_TURN_SECRET` from its own secret store, and the TURN secret has to be the
same string on both sides or every credential the server issues is rejected by the
relay.
