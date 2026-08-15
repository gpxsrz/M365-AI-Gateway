# 快速開始與首次設定

這份文件只處理「怎麼安全啟動、第一次登入、建立 API key」。架構、部署與 Hermes/Hindsight 設定請分開讀。

## 需求

- Go 版本以 `go.mod` 為準；
- 有權使用 Microsoft 365 Copilot 的帳號；
- 可完成 Microsoft 登入的瀏覽器。

## 本機啟動

使用一次性管理 bootstrap secret：

```bash
export M365_ADMIN_PASSWORD='replace-with-a-unique-bootstrap-secret'
go run ./cmd/server
```

預設只監聽：`http://127.0.0.1:4141`。

## 首次設定

1. 開啟 `http://127.0.0.1:4141`。
2. 使用本次部署提供的一次性 bootstrap secret 登入；本機直接執行時就是 `M365_ADMIN_PASSWORD`。
3. 第一次成功登入後 bootstrap secret 應失效，管理 UI 會要求設定持久管理員密碼。
4. 在管理 UI 完成 Microsoft 365 帳號登入。
5. 建立 API key。
6. 測試 model catalog：

```bash
export M365_API_KEY='replace-with-your-api-key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

不要把真實 bootstrap secret 或 API key 寫入 repo、Issue、handoff 或 log。

## Container image

```bash
docker build -t m365-ai-gateway .
```

`Dockerfile` 是建置基礎，不代表通用 Production Compose。管理 bootstrap 只信任真正 loopback；一般 bridge/NAT request 不會自動被當成 loopback。要把服務暴露到 localhost 以外，必須先設計 TLS、可信 reverse proxy、網路邊界與持久管理員密碼流程。

Container / host 部署應把 `M365_DATA_DIR` 指向可寫、持久化的資料卷。未指定 `M365_DEBUG_LOG` 時，診斷摘要會使用 data/settings directory 下的 `debug-logs.json`。

## 下一步

- API / 架構：[`architecture.md`](architecture.md)
- Runtime setting keys：[`runtime-settings.md`](runtime-settings.md)
- Hermes / Hindsight：[`hermes-hindsight.md`](hermes-hindsight.md)
- Production 原則：[`deployment.md`](deployment.md)
- 安全：[`../../SECURITY.md`](../../SECURITY.md)
