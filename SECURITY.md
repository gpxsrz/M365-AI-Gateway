# 安全性說明

## 30 秒看懂

> 一般使用者先套用這四條就停。只有你的工作碰到登入、檔案、對外開放或限流時，才讀對應小節。

1. 只使用你有權使用的 Microsoft 365 帳號與資料。
2. 預設維持 `127.0.0.1` 本機存取；對外開放前要有 TLS、可信 reverse proxy 與網路限制。
3. 不公開任何可登入、可重播 session、可識別帳號／租戶或可下載私人內容的資料。
4. Private mode 是「要求上游不要建立一般聊天歷史」，不是「Microsoft 零保留」。

## 登入與 secret

不可公開：

- 管理密碼、API key、access token、refresh credential、cookie；
- token cache、HAR、authorization header；
- 帳號、租戶或個人識別資訊；
- 完整 callback、登入錯誤頁或任何可重播 OAuth 資料。

Gateway 保存主要 Microsoft 登入需要的 credential。需要受保護檔案時，才從同一登入取得短效資源 token；不應為檔案流程建立第二份長期登入 authority。

若 Microsoft 帳號 identity 改變，舊帳號的本機檔案能力、checkpoint 與 credential 不能直接沿用。

## Private mode 與資料邊界

Private mode 會在 ChatHub transport 要求關閉一般聊天記憶。它不改變下列事實：

- 文件或圖片可能使用 Microsoft 的 OneDrive／SharePoint 或其他檔案 transport；
- Code Interpreter 產出有自己的 artifact lifecycle；
- Microsoft 仍可能依其服務政策處理或保留資料。

因此聊天、附件、圖片、artifact 與 checkpoint 要分開看，不要把「Private」解讀成所有資料層都不存在。

## 檔案與下載能力

受保護的 Microsoft artifact 不直接把上游私密 URL 交給 caller。Gateway 會：

1. 驗證上游 host/path 是否在允許範圍；
2. 用已登入狀態取回檔案；
3. 放進本機 private store；
4. 對 caller 提供短效 capability URL。

Capability 本身就是下載權限，不要寫進 log、Issue、文件或聊天。

## 對外開放

預設 local-only 是安全邊界的一部分。若要透過 reverse proxy 提供服務：

- 使用 TLS；
- 明確設定可信 management host / origin；
- 不信任任意 forwarded header；
- 用防火牆或網路 ACL 限縮來源；
- 不把管理面直接暴露到不受控網路。

公開部署原則見 [`docs/zh-TW/deployment.md`](docs/zh-TW/deployment.md)。私人 Production host/path/credential 不屬於 public repo 文件。

## 共用帳號與 429

一個 Gateway 只代表一個 Microsoft 365 帳號。Hermes、Hindsight 與其他 caller 可以共享它，但 Gateway 會限制併發、排隊並使用 shared breaker。

不要用真實帳號故意製造高併發來探索限流。429、breaker、recovery 與 retry 應先用 deterministic test 驗證；需要 live probe 時保持低頻、可停止、可對帳。

## 回報安全問題

請私下聯絡維護者並提供最小重現、影響範圍與 source version。先遮蔽所有敏感資料；不要把 credential、個資、私有 URL、artifact 內容或可重播封包放進公開 Issue／討論。

---

# Security

## Understand it in 30 seconds

> Most users can apply these four rules and stop. Read the sign-in, file, exposure, or throttling section only when that surface is relevant.

1. Use only Microsoft 365 accounts and data you are authorized to access.
2. Keep the default `127.0.0.1` local boundary unless you add TLS, a trusted reverse proxy, and network restrictions.
3. Never publish anything that can authenticate, replay a session, identify an account/tenant, or download private content.
4. Private mode means “ask the upstream not to create ordinary chat history,” not “Microsoft retains nothing.”

## Sign-in and secrets

Never publish:

- administrator passwords, API keys, access tokens, refresh credentials, or cookies;
- token caches, HAR files, or authorization headers;
- account, tenant, or personal identifiers;
- complete callbacks, sign-in error pages, or replayable OAuth material.

The gateway keeps the credential needed for the primary Microsoft sign-in. Protected-file flows obtain short-lived resource tokens from that same sign-in instead of creating a second long-lived authentication authority.

If the Microsoft account identity changes, old local file capabilities, checkpoints, and credentials must not be reused blindly.

## Private mode and data boundaries

Private mode asks ChatHub transport to disable ordinary chat memory. It does not change these facts:

- documents and images may use OneDrive, SharePoint, or other Microsoft file transport;
- Code Interpreter output has a separate artifact lifecycle;
- Microsoft may still process or retain data under its service policies.

Treat chat, attachments, images, artifacts, and checkpoints as separate boundaries.

## Files and download capabilities

The gateway does not expose protected Microsoft artifact URLs directly. It:

1. validates the upstream host and path;
2. fetches the file with authenticated state;
3. stores it in a private local area;
4. gives the caller a short-lived capability URL.

The capability itself authorizes a download. Never put it in logs, Issues, docs, or chat transcripts.

## Network exposure

Local-only binding is part of the default security boundary. If you expose the service through a reverse proxy:

- use TLS;
- explicitly configure trusted management host/origin behavior;
- do not trust arbitrary forwarded headers;
- restrict source networks with firewall or ACL rules;
- do not expose the management surface directly to an uncontrolled network.

See [`docs/en/deployment.md`](docs/en/deployment.md) for public deployment principles. Private Production hosts, paths, and credentials do not belong in the public repository docs.

## Shared-account throttling

One gateway represents one Microsoft 365 account. Hermes, Hindsight, and other callers may share it, but the gateway limits concurrency, queues requests, and uses a shared breaker.

Do not deliberately stress a real account to discover rate limits. Prove 429, breaker, recovery, and retry behavior deterministically first; keep any necessary live probe low-rate, stoppable, and auditable.

## Report a security issue

Contact the maintainer privately with a minimal reproduction, impact, and source version. Redact sensitive material first. Do not place credentials, personal data, private URLs, artifact contents, or replayable packets in public Issues or discussions.
