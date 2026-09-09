# 快速開始與第一次設定

## 30 秒看懂

> 第一次安裝只做到「確認 API 可用」就停。遇到部署、Hermes、Hindsight 或精確 API 問題，再從文件路由開對應頁。

你只需要：

1. 啟動 Gateway；
2. 換掉一次性管理密碼；
3. 完成一次 Microsoft 登入；
4. 建立 API key；
5. 用 `/v1/models` 做本機 smoke。

預設管理網址是 `http://127.0.0.1:4141`，只給同一台電腦使用。

## 開始前確認

- 安裝 `Cargo.toml` 指定的 Rust toolchain。
- 你有權使用一個 Microsoft 365 Copilot 帳號。
- 這台電腦可以開瀏覽器完成 Microsoft 登入。
- 不要把 password、API key、token、callback 或 cookie 貼到聊天、Issue 或 repo。

## 1. 啟動 Gateway

先提供只用一次的管理密碼：

```bash
export M365_ADMIN_PASSWORD='請換成只用一次的管理密碼'
cargo run --locked --bin m365-native
```

服務啟動後，開啟：

```text
http://127.0.0.1:4141
```

## 2. 完成管理設定

1. 用一次性密碼登入。
2. 依畫面要求設定正式管理密碼，然後重新登入。
3. 從管理頁啟動 Microsoft 登入。
4. 在受控瀏覽器完成一次 Microsoft 帳號登入。
5. 回管理頁確認 account state 可用。
6. 建立 API key；原始 key 只顯示一次，立即存到安全位置。

### Microsoft 登入邊界

正常流程只有一份主要 Microsoft 登入。需要 Code Interpreter 檔案時，Gateway 會從同一登入取得短效資源 token，不需要再建立第二份長期 Teams/browser credential。

受控瀏覽器的登入狀態和你平常使用的瀏覽器分開；第一次使用可能仍要輸入帳密或完成 MFA。

若管理頁提供相容備援登入，照 UI 操作即可。不要把 callback、authorization code、referrer 或完整 Microsoft error page 複製到外部工具。

## 3. 確認 API 可用

把 API key 放進目前 shell，不寫入 repo：

```bash
export M365_API_KEY='請換成剛建立的 API key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

看到模型清單表示：

- Gateway listener 可達；
- API key 驗證成功；
- current model catalog 可以投影。

這**不表示**真實聊天、圖片、Code Interpreter、所有 Web capability 或 Production 都已驗證。

## Container

可用 repo Dockerfile 建置：

```bash
docker build -t m365-ai-gateway .
```

實際運行要把 `M365_DATA_DIR` 指到可寫且持久的 volume。Dockerfile 是可重現建置基礎，不是任何環境都能原樣套用的 Production SOP。

若要從其他電腦連線，不要只把 listener 改成 `0.0.0.0` 就結束；先讀 [`deployment.md`](deployment.md) 與 [`../../SECURITY.md`](../../SECURITY.md)。

## 卡住時先看

| 現象 | 先確認 |
|---|---|
| 管理頁打不開 | process 是否仍在跑、網址是否為 `127.0.0.1:4141` |
| 管理登入 403 | management origin / host 是否符合目前安全設定 |
| 一次性密碼失效 | 成功 bootstrap 後即失效是預期行為 |
| API 401 | Bearer API key 是否正確、是否已撤銷 |
| Microsoft 登入停住 | 受控瀏覽器是否仍等待登入／MFA；不要重複啟多個登入流程 |
| 登入正常但 protected file 失敗 | 不要再做第二次 Teams OAuth；查 artifact / token transport error |
| `/v1/models` 成功但聊天失敗 | Models smoke 只驗 local API surface；再查 API error contract |

## 接著讀哪裡

- 系統怎麼運作：[`architecture.md`](architecture.md)
- Hermes / Hindsight：[`hermes-hindsight.md`](hermes-hindsight.md)
- 精確 API 錯誤：[`api-contracts.md`](api-contracts.md)
- 部署：[`deployment.md`](deployment.md)
- Runtime 設定：[`runtime-settings.md`](runtime-settings.md)
