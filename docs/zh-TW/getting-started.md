# 快速開始與第一次設定

## 30 秒看懂

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
3. 在管理頁面啟動 Microsoft 登入，並在瀏覽器完成授權。
4. 建立 API key。原始 key 只會顯示一次，請立即存到安全位置。

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
| Microsoft 登入未完成 | 回到管理頁看狀態；不要把 callback、token 或錯誤全文貼到公開紀錄 |

## 下一頁

- 想了解資料怎麼走：[`architecture.md`](architecture.md)
- 想接 Hermes / Hindsight：[`hermes-hindsight.md`](hermes-hindsight.md)
- 想部署：[`deployment.md`](deployment.md)
- 想查設定：[`runtime-settings.md`](runtime-settings.md)
