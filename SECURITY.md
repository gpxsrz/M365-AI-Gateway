# 安全性說明

此公開快照只適用於你有權使用的 Microsoft 365 帳號與租戶環境。

## 最低限度安全原則

- 預設只綁定在 `127.0.0.1`；若要對外暴露，請自行補上可信的認證、TLS、反向代理與網路邊界控管。
- 不要在任何公開紀錄中提交 token、cookies、HAR、OneDrive / SharePoint 私有連結、artifact 原始位址或帳號快取。
- `Private / Temporary Chat` 只代表 no-ordinary-history transport policy，不代表 Microsoft 完全不保留任何資料；一般文件上傳仍可能建立 OneDrive / SharePoint staging copy。
- `Code Interpreter` 產生的 artifact 若要回傳給 consumer，必須走 authenticated artifact fetch；不要直接暴露瀏覽器 `blob:` URL 或未驗證的上游暫時連結。
- Hermes 與 Hindsight 可以共用同一個 Microsoft 365 帳號，但 checkpoint 邊界與相容入口必須彼此隔離。帳號級吞吐量仍然共用，因此新的 Memory 背景工作必須讓位給互動式 `/v1/chat/completions` 與 `/hermes/v1/chat/completions` 流量，不應用大量併發壓 ChatHub。
- 不要用真實 Microsoft 帳號故意製造高併發來探 429 上限；429／退避行為應以本地 deterministic test 驗證，線上驗證維持低併發。

## 回報方式

若你在這個衍生快照中發現安全問題，請直接私下聯絡目前維護者，附上最小重現資訊、影響範圍與版本描述，並先自行遮蔽所有敏感資料。

請不要把私密憑證、個資或可重放的封包直接貼進公開議題、公開討論串或上游專案。

---

# Security

This public snapshot is intended only for Microsoft 365 accounts and tenants you are authorized to use.

## Minimum security requirements

- Bind to `127.0.0.1` by default. Before exposing the service externally, add trusted authentication, TLS, reverse proxy controls, and network access boundaries.
- Never publish tokens, cookies, HAR files, private OneDrive / SharePoint URLs, original artifact URLs, or account caches.
- `Private / Temporary Chat` means a no-ordinary-history transport policy; it does not mean Microsoft retains no data. Ordinary document uploads may still create OneDrive / SharePoint staging copies.
- Code Interpreter artifacts returned to consumers must use authenticated artifact fetch. Never expose browser `blob:` URLs or unverified upstream temporary links directly.
- Hermes and Hindsight may share one Microsoft 365 account, but their checkpoint boundaries and compatibility endpoints must remain isolated. Account-level throughput is still shared, so Memory background work must yield to interactive `/v1/chat/completions` and `/hermes/v1/chat/completions` traffic rather than intentionally stress ChatHub.
- Do not deliberately create high concurrency against a real Microsoft account to discover rate limits. Verify 429/backoff behavior with deterministic local tests and keep live validation low-concurrency.

## Reporting

If you find a security issue in this derivative snapshot, contact the current maintainer privately with minimal reproduction information, impact scope, and version details. Redact all sensitive information before sending it.

Do not post private credentials, personal data, or replayable packets in public issues, public discussions, or upstream projects.
