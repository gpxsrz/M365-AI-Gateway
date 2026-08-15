# Getting started and first-time setup

This document covers safe startup, the first administrator login, Microsoft sign-in, and API-key creation only. Load architecture, deployment, or Hermes/Hindsight docs separately when needed.

## Requirements

- Use the Go version declared by `go.mod`.
- Use a Microsoft 365 account that is authorized for Copilot.
- Have a browser available for Microsoft sign-in.

## Local startup

Provide a one-time administrator bootstrap secret:

```bash
export M365_ADMIN_PASSWORD='replace-with-a-unique-bootstrap-secret'
go run ./cmd/server
```

The service binds to `http://127.0.0.1:4141` by default.

## First-time setup

1. Open `http://127.0.0.1:4141`.
2. Sign in with the deployment bootstrap secret; for direct local execution this is `M365_ADMIN_PASSWORD`.
3. After the first successful login, the bootstrap secret should become invalid and the management UI requires a persistent administrator password.
4. Complete Microsoft 365 account sign-in in the management UI.
5. Create an API key.
6. Test the model catalog:

```bash
export M365_API_KEY='replace-with-your-api-key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

Never put real bootstrap secrets or API keys in the repository, Issues, handoff files, or logs.

## Container image

```bash
docker build -t m365-ai-gateway .
```

The `Dockerfile` is a build base, not a universal Production Compose recipe. Administrator bootstrap trusts true loopback only; ordinary bridge/NAT requests do not automatically count as loopback. Before exposing the service beyond localhost, design TLS, trusted reverse-proxy controls, network boundaries, and persistent administrator-password handling.

Container or host deployments should point `M365_DATA_DIR` at writable persistent storage. When `M365_DEBUG_LOG` is not set, diagnostic summaries use `debug-logs.json` under the data/settings directory.

## Next

- API / architecture: [`architecture.md`](architecture.md)
- Runtime setting keys: [`runtime-settings.md`](runtime-settings.md)
- Hermes / Hindsight: [`hermes-hindsight.md`](hermes-hindsight.md)
- Production principles: [`deployment.md`](deployment.md)
- Security: [`../../SECURITY.md`](../../SECURITY.md)
