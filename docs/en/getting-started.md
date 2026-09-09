# Getting started and first-time setup

## Understand it in 30 seconds

> For a first installation, stop after **Verify API access**. Open the deployment, Hermes, Hindsight, or exact-API topic only when that surface becomes relevant.

You need to:

1. start the gateway;
2. replace the one-time administrator password;
3. complete one Microsoft sign-in;
4. create an API key;
5. run a local `/v1/models` smoke check.

The default management URL is `http://127.0.0.1:4141` and is intended for the same machine only.

## Before you start

- Install the Rust toolchain declared by `Cargo.toml`.
- Use a Microsoft 365 Copilot account you are authorized to access.
- Make sure this computer can open a browser to complete Microsoft sign-in.
- Never copy passwords, API keys, tokens, callbacks, or cookies into chat, Issues, or the repository.

## 1. Start the gateway

Provide a one-time administrator password:

```bash
export M365_ADMIN_PASSWORD='replace-with-a-one-time-admin-password'
cargo run --locked --bin m365-native
```

When the service is running, open:

```text
http://127.0.0.1:4141
```

## 2. Finish management setup

1. Sign in with the one-time password.
2. Replace it with a persistent administrator password when prompted, then sign in again.
3. Start Microsoft sign-in from the management page.
4. Complete one Microsoft account sign-in in the controlled browser.
5. Return to the management page and confirm that account state is usable.
6. Create an API key. The raw key is displayed only once; store it securely immediately.

### Microsoft sign-in boundary

The normal flow has one primary Microsoft sign-in. When a Code Interpreter file is needed, the gateway obtains a short-lived resource token from that same sign-in instead of creating a second long-lived Teams/browser credential.

The controlled browser has its own sign-in state. On first use you may still need to enter credentials or complete MFA even if your ordinary browser is already signed in.

If the management UI offers a compatibility fallback sign-in, follow the UI. Do not copy a callback, authorization code, referrer, or complete Microsoft error page into another tool.

## 3. Verify API access

Put the API key into the current shell, not the repository:

```bash
export M365_API_KEY='replace-with-the-created-api-key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

A model list proves only that:

- the gateway listener is reachable;
- API-key authentication succeeded;
- the current model catalog can be projected.

It does **not** prove real chat, images, Code Interpreter, every Web capability, or Production.

## Containers

Build the repository Dockerfile with:

```bash
docker build -t m365-ai-gateway .
```

At runtime, point `M365_DATA_DIR` at a writable persistent volume. The Dockerfile is a reproducible build base, not a complete Production SOP for every environment.

If another computer must reach the service, do not stop at changing the listener to `0.0.0.0`. Read [`deployment.md`](deployment.md) and [`../../SECURITY.md`](../../SECURITY.md) first.

## Troubleshooting first checks

| Symptom | Check first |
|---|---|
| Management page does not open | the process is still running and the URL is `127.0.0.1:4141` |
| Management login returns 403 | management origin / host matches the trusted configuration |
| One-time password no longer works | successful bootstrap intentionally consumes it |
| API returns 401 | Bearer API key is valid and not revoked |
| Microsoft sign-in appears stuck | controlled browser is still waiting for sign-in/MFA; do not start overlapping sign-in flows |
| Sign-in works but a protected file fails | do not perform a second Teams OAuth; inspect artifact/resource-token transport errors |
| `/v1/models` succeeds but chat fails | model-list smoke proves only the local API surface; inspect the API error contract next |

## Read next

- How the system works: [`architecture.md`](architecture.md)
- Hermes / Hindsight: [`hermes-hindsight.md`](hermes-hindsight.md)
- Exact API errors: [`api-contracts.md`](api-contracts.md)
- Deployment: [`deployment.md`](deployment.md)
- Runtime settings: [`runtime-settings.md`](runtime-settings.md)
