# WP5 OAuth 線上驗證計畫

## 狀態與授權邊界

WP5-03 已完成可重現的離線生命週期測試，但 **WP5-03 未執行正式 Microsoft 線上驗收**。後續若要執行真實 Browser PKCE 驗證，必須由維護者針對當次瀏覽器操作**明確授權**；測試成功不代表自動允許 Production 部署、正式 profile promotion、Git publication 或任何 Hermes 變更。

本計畫只描述隔離環境中的 Browser PKCE 驗證方式。Production、Hermes Agent、Hermes Gateway、provider/model 設定、channel override 與既有正式 OAuth profile 都必須保持不變。**Production 重新啟動不在此計畫範圍**。

## 隱私與資料紀錄原則

只記錄無法重用的、公開安全的技術資訊，例如：

- source revision 與測試 harness revision；
- 隔離 binary 的 SHA-256 與 `vcs.modified=false`；
- opaque staged profile ID 與 pointer generation；
- token-cache schema version；
- stable error code、布林值、計數與時間戳。

**不得記錄 Client ID 文字**、帳號 email、OID、TID、authorization code、PKCE verifier、access token、refresh token、ID token、Cookie、Authorization header、callback query、完整 prompt／response 或附件內容。

## 隔離測試前提

1. 使用專案既定的 Microsoft first-party Browser PKCE 路徑與有權使用的測試帳號。
2. 使用獨立 data directory 與獨立 Sidecar listener，不得指向正式 token cache。
3. OAuth candidate 必須由 Sidecar 自己建立；**不得加入公開的 `/api/auth/start` 覆寫**來改 Client ID、authority、redirect、scope 或 token endpoint。
4. Browser capture 必須是可見且可由使用者控制的環境；MFA 或其他真人互動仍由使用者完成。
5. staged profile 在 promotion 前不得成為正式 active profile。
6. 驗證期間不得修改正式 Production 或 Hermes。

## 驗證流程

### 1. 首次登入

- 從空的隔離 profile 或全新的 staged candidate 啟動 Browser PKCE。
- 只接受一次與交易綁定的 callback。
- 驗證 callback 不回傳 token 或帳號識別資訊。
- 驗證 token store schema 與私有檔案權限符合預期。

預期結果：`first_login / success`。

### 2. 階段式重新授權

- 使用 `stageActive=true` 建立 staged profile。
- 完成同一測試帳號的重新授權。
- 只允許 staged token store 改變；正式 active profile 不得被改寫。

預期結果：`reauth / success`。

### 3. Callback 負向狀態

分別驗證：

- 使用者取消；
- transaction timeout；
- state mismatch；
- callback replay。

必須得到穩定錯誤碼、exactly-once token exchange，且 log 中不得出現 callback secret material。

### 4. ChatHub 驗證

只使用 staged profile 執行一次最小、非破壞性的 ChatHub request。Server-side evidence 必須能證明 request 使用的是 staged profile；不保存完整 prompt 或 response。

### 5. Refresh 成功

- 讓 staged access token 進入需要 refresh 的狀態。
- 只透過 staged profile store 執行 refresh。
- 驗證 staged account 恢復 online，正式 active profile bytes 保持不變。

預期結果：`refresh_success / success`。

### 6. Refresh 失敗與復原

- 在另一個 staged profile 製造可預期的 refresh failure，例如測試 refresh token 已失效。
- 驗證只影響該 staged profile，並回傳穩定 `token_refresh_error`。
- 重新建立新的 staged profile 完成授權；不得覆寫或直接 promotion 失敗的 profile。

預期結果：`refresh_failure / failed / token_refresh_error`，之後再取得新的 `reauth / success`。

### 7. 重新啟動持久性

只重新啟動隔離測試 Sidecar。重新從磁碟開啟 profile manager，確認 staged manifest、token store、opaque account refs、validation state 與 pointer generation 仍一致。

### 8. 移除帳號

透過管理端點從 staged profile 移除測試帳號，驗證 staged account 與 opaque reference 退役，而正式 active profile 保持不變。如需重新建立帳號，必須重新走 Browser PKCE，不可直接回填 token store。

### 9. Promotion

只有同一 staged profile 已完成 ChatHub、refresh、restart persistence 與 account removal 驗證後，才可在隔離測試環境做 promotion。Promotion 只允許原子 pointer 切換與 generation 增加，不得重寫 token store。

### 10. 回復（Rollback）

隔離 Sidecar 在 promoted pointer 上重新啟動後，再執行 rollback。驗證 rollback 只改變 pointer 與 generation，並恢復前一個 opaque profile ID；兩份 token store 都必須維持原始 bytes。

## 立即停止條件

遇到以下任一情況就停止，不做 promotion：

- callback 無法在不暴露 secret material 的前提下安全取得；
- redirect URI 或 transaction binding 不符合預期；
- callback state、replay 或 expiry 行為與契約不一致；
- token/profile 內容出現在 log 或 evidence；
- ChatHub、refresh、restart persistence 或 account removal 無法穩定重現；
- 正式 active profile 或 pointer 在授權前發生改變；
- 任何 Production 或 Hermes invariant 發生變化。

## 公開證據格式

公開結果只保留 schema version、source/harness identity、stable error code、opaque ID、pointer generation、計數、布林值與時間戳。任何含 authorization code、verifier、token、Cookie、email、OID、TID、account mapping、完整 request/response 或附件內容的資料都不得進入公開 evidence。
