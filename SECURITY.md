# 安全性說明

## 先記住三件事

> AI Agent：先套用這三條。只有要處理檔案、Private mode 或流量時，才讀對應小節；任何秘密都不要讀回到輸出。

1. 只使用你有權使用的 Microsoft 365 帳號與資料。
2. 預設只在本機 `127.0.0.1` 使用。對外開放前，要先有 TLS、可信反向代理與網路限制。
3. 不要公開任何可登入、可重播或可下載私人內容的資料。

## 不可公開的資料

- 密碼、API key、token、cookie、HAR 或 token cache。
- 帳號、租戶與個人識別資訊。
- OneDrive／SharePoint 私有網址。
- Microsoft 暫時檔案網址、Code Interpreter 原始網址或產出檔案內容。
- 完整錯誤回應或封包，只要其中可能帶有上述資料。

Gateway 會先用已登入狀態取得受保護檔案，再存到本機私有區域，最後提供短效下載能力。不要直接把 Microsoft 或瀏覽器的暫時網址交給呼叫端。

Gateway 只保存主要 Microsoft 登入的更新憑證。需要下載 Code Interpreter 檔案時，它才用同一份憑證換取短效 IC3 資源 token；不需要第二次瀏覽器授權，也不保存另一份 Teams 更新憑證。帳號識別若改變，舊帳號的登入狀態與本機檔案能力都不得沿用。

## Private mode 的真正意思

Private mode 會要求上游不要建立一般聊天記錄。這不等於零保留：文件或圖片仍可能經過 OneDrive／SharePoint 暫存，Microsoft 也可能依其服務政策處理資料。

## 共用帳號流量

Hermes、Hindsight 與一般呼叫可以共用同一帳號，但 Gateway 會限制同時請求數並安排優先順序。不要用真實帳號故意製造高併發來測 429。限流與退避應先用本機測試驗證，線上測試保持低頻、可停止。

## 回報安全問題

請私下聯絡維護者，提供最小重現步驟、影響範圍與版本。先遮蔽所有敏感資料；不要把私密憑證、個資或可重播封包放進公開 Issue 或討論。

---

# Security

## Remember three things

> AI agents: apply these three rules first. Read the file, Private-mode, or traffic sections only when that surface is in scope, and never copy secrets into output.

1. Use only Microsoft 365 accounts and data you are authorized to access.
2. Keep the default `127.0.0.1` local binding. Add TLS, a trusted reverse proxy, and network restrictions before exposing the service.
3. Never publish data that can authenticate, replay a session, or download private content.

## Data that must stay private

- Passwords, API keys, tokens, cookies, HAR files, and token caches.
- Account, tenant, or personal identifiers.
- Private OneDrive or SharePoint URLs.
- Microsoft temporary file URLs, raw Code Interpreter URLs, or generated file contents.
- Full error bodies or packets when they may contain any of the above.

The gateway fetches protected files with authenticated state, stores them in a private local area, and exposes a short-lived download capability. Do not return Microsoft or browser temporary URLs directly to callers.

The gateway stores only the primary Microsoft sign-in refresh credential. When a Code Interpreter file is needed, it uses that same credential to obtain a short-lived IC3 resource token. There is no second browser authorization or separate Teams refresh credential. If the account identity changes, neither the old sign-in state nor its local file capabilities may be reused.

## What Private mode means

Private mode asks the upstream service not to create ordinary chat history. It does not mean zero retention. Documents and images may still use OneDrive or SharePoint staging, and Microsoft may process data under its service policies.

## Shared-account traffic

Hermes, Hindsight, and ordinary callers may share one account. The gateway limits concurrent requests and orders the work. Do not deliberately stress a real account to discover 429 limits. Verify throttling and backoff locally first; keep live checks low-rate and stoppable.

## Report a security issue

Contact the maintainer privately with minimal reproduction steps, impact, and version. Redact sensitive data first. Do not put credentials, personal data, or replayable packets in public Issues or discussions.
