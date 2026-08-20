# Getting started and first-time setup

## Understand it in 30 seconds

You need four steps: start the gateway, replace the one-time administrator password, sign in to Microsoft, and create an API key.

The default address is `http://127.0.0.1:4141`, which accepts connections from the same computer only.

## Before you start

- Install the Rust version declared in `Cargo.toml`.
- Use a Microsoft 365 Copilot account you are authorized to access.
- Make sure this computer can open a browser for Microsoft sign-in.

## Step 1: start the service

Set a one-time administrator password:

```bash
export M365_ADMIN_PASSWORD='replace-with-a-one-time-admin-password'
cargo run --locked --bin m365-native
```

When the service reports that it is listening on `127.0.0.1:4141`, open `http://127.0.0.1:4141`.

## Step 2: finish setup

1. Sign in with the one-time password.
2. Replace it when prompted, then sign in again with the new password.
3. Select **Automatically Sign in to Microsoft Account** and finish sign-in in the controlled Chrome window.
4. Create an API key. The raw key is shown once, so store it safely immediately.

### Choose a Microsoft sign-in path

Start with **Automatic Sign-in**. One click completes two steps in the same controlled Chrome window:

1. Capture the Microsoft chat sign-in result.
2. Capture the Teams file-access result. Files created by Code Interpreter need this step.

The page may switch quickly. Keep the window open until the management page reports completion. The gateway receives only the one-time sign-in results; it does not store the full error page.

On first use, you may still need to sign in once inside that window. It then keeps its own browser session.

Your regular Chrome session is not copied into the controlled Chrome. If you are already signed in to regular Chrome, expand **Use compatibility fallback**:

1. Select **Use Current Browser Session**.
2. Microsoft may show “This isn't the right page.” The authorization result is still available.
3. A trusted local AI agent can read the error page's `referrer` without displaying it and submit it directly to the local gateway.

This fallback completes the main chat sign-in only. To download files created by Code Interpreter, complete **Automatic Sign-in** successfully at least once.

Never paste the callback URL, authorization code, or complete error page into chat, logs, or documentation.

## Step 3: verify local access

Put the API key in the current shell. Do not write it into the repository:

```bash
export M365_API_KEY='replace-with-the-new-api-key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

A model list proves that the local gateway, administrator flow, and API key work. It does not prove live chat yet; that requires a separate low-rate chat request.

## Use a container

```bash
docker build -t m365-ai-gateway .
```

Point `M365_DATA_DIR` at writable persistent storage. The `Dockerfile` is a safe build base, not a universal Production configuration.

First-use administrator bootstrap trusts real loopback only. A container bridge or NAT connection does not automatically count as local. Read [Deployment and reverse proxy](deployment.md) before allowing another computer to connect.

## If you get stuck

| Symptom | Check first |
|---|---|
| The management page does not open | The process is still running and the address is `127.0.0.1:4141` |
| Sign-in returns 403 | The request has exactly one correct `Origin`, and proxy trust is configured correctly |
| The one-time password no longer works | This is expected; use the persistent password you created |
| The API returns 401 | `Authorization: Bearer ...` contains a valid API key |
| Automatic sign-in stops at a Microsoft sign-in field | First use requires a separate sign-in in the controlled Chrome; the regular Chrome session is not copied |
| The page briefly switches to Teams after sign-in | This is the file-access step; keep the window open until the management page reports completion |
| Teams file permission fails | Make sure the controlled Chrome uses the same Microsoft account, then restart sign-in from the management page |
| Microsoft shows an error page | Return to the management page for status; automatic mode captures before the error page, while a trusted local agent may securely submit the `referrer` for the compatibility fallback |
| Microsoft sign-in did not finish | Close any controlled Chrome window that is still waiting, then start again; never share callbacks, tokens, or full error bodies |

## Next page

- Understand the data flow: [`architecture.md`](architecture.md)
- Connect Hermes / Hindsight: [`hermes-hindsight.md`](hermes-hindsight.md)
- Deploy: [`deployment.md`](deployment.md)
- Look up settings: [`runtime-settings.md`](runtime-settings.md)
