# 快速開始與第一次設定

## 30 秒看懂

> AI Agent：第一次安裝只讀到「第三步」。只有遇到錯誤才展開「卡住時先看」；不要先載入部署或內部協定。

你只需要做四件事：啟動 Gateway、換掉一次性管理密碼、登入 Microsoft、建立 API key。

預設網址是 `http://127.0.0.1:4141`，只讓同一台電腦連線。

## 開始前確認

- 已安裝 `Cargo.toml` 指定的 Rust 版本。
- 你有權使用一個 Microsoft 365 Copilot 帳號。
- 這台電腦能開啟瀏覽器完成 Microsoft 登入。

## 第一步：啟動

先設定一個只用一次的管理密碼：

```bash
export M365_ADMIN_PASSWORD='請換成只用一次的管理密碼'
cargo run --locked --bin m365-native
```

看到服務監聽 `127.0.0.1:4141` 後，開啟 `http://127.0.0.1:4141`。

## 第二步：完成設定

1. 用剛才的一次性密碼登入。
2. 依畫面要求換成正式管理密碼。換完後要重新登入。
3. 在管理頁按「自動登入 Microsoft 帳號」，並在受控 Chrome 完成登入。
4. 建立 API key。原始 key 只會顯示一次，請立即存到安全位置。

### Microsoft 登入怎麼選

先用「自動登入」。按一次後，Gateway 會開啟受控 Chrome，接住同一筆 Microsoft 登入結果。沒有第二段 Teams 授權。

頁面可能會短暫顯示 Microsoft 的完成頁或錯誤頁。完成前不要關閉視窗。自動流程會在可用資訊消失前擷取一次性登入結果；Gateway 不保存完整錯誤頁。

第一次使用時，你仍可能要在這個視窗登入一次。之後它會保留自己的登入狀態。

一般 Chrome 的登入狀態不會複製到受控 Chrome。若你已在一般 Chrome 登入，可展開「使用相容備援」：

1. 按「使用目前瀏覽器登入」。
2. Microsoft 可能顯示「這不是正確的頁面」。這不代表授權資料消失。
3. 受信任的本機 AI Agent 可以讀取錯誤頁的 `referrer`，不顯示內容，直接送回本機 Gateway。

這條備援完成的也是同一份主要登入。需要 Code Interpreter 檔案時，Gateway 會用這份更新憑證自動換取短效 IC3 token，不會再要求一次 Teams 登入。

不要把 callback 網址、授權碼或錯誤頁全文貼到聊天、日誌或文件。

## 第三步：確認可以使用

先把 API key 放進目前的 shell，不要寫進 repo：

```bash
export M365_API_KEY='請換成剛建立的 API key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

看到模型清單就代表本機 Gateway、管理流程與 API key 都能運作。這一步不等於已測過真實聊天；聊天驗證要另外送一筆低頻請求。

## 使用 Container

```bash
docker build -t m365-ai-gateway .
```

Container 要把 `M365_DATA_DIR` 指到可寫、會保留的資料卷。`Dockerfile` 只是安全建置基礎，不是任何環境都能直接套用的 Production 設定。

管理密碼的第一次啟用只信任真正的 loopback。Container bridge 或 NAT 不會自動被當成本機。若要讓別台電腦連線，先讀[部署與反向代理](deployment.md)。

## 卡住時先看

| 現象 | 先確認 |
|---|---|
| 無法開啟管理頁 | 程序仍在執行，且網址是 `127.0.0.1:4141` |
| 登入回 403 | request 是否只有一個正確的 `Origin`，反向代理設定是否可信 |
| 一次性密碼不能再用 | 這是預期行為；請用換好的正式密碼 |
| API 回 401 | `Authorization: Bearer ...` 是否使用有效 API key |
| 自動登入停在 Microsoft 登入欄 | 受控 Chrome 第一次使用要另外登入；一般 Chrome 的登入不會自動複製 |
| 按登入後看似沒有反應 | 等待受控 Chrome 開啟；若已開啟，完成其中的 Microsoft 登入，再回管理頁看狀態 |
| 登入完成但檔案下載失敗 | 不要重做第二次 Teams 授權；先確認帳號仍在線，再查 Gateway 的安全錯誤碼 |
| Microsoft 顯示錯誤頁 | 回到管理頁看狀態；自動模式會在錯誤頁前擷取，相容備援可由受信任的本機 Agent 安全送回 `referrer` |
| Microsoft 登入未完成 | 關閉仍在等待的受控 Chrome 後重新開始；不要分享 callback、token 或錯誤全文 |

## 下一頁

- 想了解資料怎麼走：[`architecture.md`](architecture.md)
- 想接 Hermes / Hindsight：[`hermes-hindsight.md`](hermes-hindsight.md)
- 想部署：[`deployment.md`](deployment.md)
- 想查設定：[`runtime-settings.md`](runtime-settings.md)
