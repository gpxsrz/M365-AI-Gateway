# Rust 改寫對照

## 30 秒看懂

> AI Agent：先讀「這次抓到的偏差」。要查單一功能才讀對照表；要發布才讀最後的 gate。不要把保留的 Go 程式當成建置來源。

Rust 是唯一 release／container 建置來源。Go `f038c86e62c7390c442f30043715255576db4e19` 保留為唯讀比較基準，用來回答「原本真的怎麼做」，不能憑印象補流程。

目前已對到的核心原則：

- 一個 Gateway 只對一個 Microsoft 365 帳號。
- Microsoft 瀏覽器登入只有一次。
- 檔案需要的 IC3 token 由同一份主要更新憑證取得。
- ChatHub payload、stream、tools、checkpoint 與錯誤形狀要由測試鎖住。
- 本機 PASS、live PASS、CI 與 Production 是四個不同 gate。

## 這次抓到的偏差

先前 Rust 版本自行加入第二段 Teams OAuth。這不在原 Go 流程裡，因此使用者按一次登入後，畫面會無聲等待第二段授權。

另一個問題是 artifact 測試只證明「找到 metadata」，沒有證明「真的讀到檔案 bytes」。真實 Microsoft 回傳的網址在 `/views/original` 後多一個顯示檔名；直接抓整條網址會 404，下載端點要保留 query、移除那一段顯示檔名。

串流還有一個生命週期偏差：Rust 曾把上游工作放進獨立 task，呼叫端斷線後仍會占住帳號容量；原 Go request context 會跟著客戶端取消。Rust 現在也會在 response body 被丟棄時取消上游工作。

修正後的共同路徑是：

1. 只保存主要 Microsoft refresh credential。
2. 需要檔案時，用它換取同帳號的短效 IC3 access token。
3. 只接受核准的 HTTPS host 與 artifact path。
4. 只移除一個顯示檔名；更深或不明路徑一律拒絕。
5. 私密上游網址與原始 artifact event 不交給 API 呼叫端。
6. 同一份 refresh credential 的一般更新與資源 token 更新共用鎖，避免旋轉憑證互撞。

## 功能對照

| Surface | Rust 保留的契約 | 最小證據 |
|---|---|---|
| OpenAI Chat Completions | non-stream／SSE、tools、usage、單一 `[DONE]`、斷線取消 | adapter 與 route tests |
| Responses | parent、tool result、parallel calls、reasoning／media events | continuation tests |
| Anthropic Messages | error、tool／image round trip、posthoc stream | adapter tests |
| Hermes | provenance、ledger、completion guard、多輪 tools、排程 | full continuation tests |
| Hindsight | retain／recall／reflect、breaker、webhook、barrier | Memory profile tests |
| OAuth | 一次登入、帳號綁定、refresh rotation | browser + auth lifecycle tests |
| Code Interpreter | 私有暫存、短效下載、stream holdback、重啟續取 | deterministic + isolated live |
| MCP | modern HTTP 與 legacy SSE 邊界 | route tests + official Python client |
| Admin | bootstrap、密碼、API key、設定來源、redaction | HTTP tests + browser path |
| Release | pinned toolchain、locked build、Rust container | local release gate + exact-head CI |

## 發布 gate

每個候選版本都要依序完成：

1. Rust format、完整 tests、Clippy、release build、diff check。
2. 若 parity 判斷依賴 Go，跑 Go verify／test／vet／build。
3. Serena 與 Code Review Graph 檢查受影響路徑；graph 的零影響不能取代原碼搜尋。
4. Commit 後，以 exact head 跑 GitHub CI 與 container build。
5. 分別讀回 public ref、NAS、VM 與 release artifact。
6. 建立可驗證 recovery，再部署 Production。
7. 以低頻 live request、服務狀態、binary／Web hash 與 rollback 證據收尾。

任何一步失敗，都只能回報部分完成。精確結果放在 CI、Git history 與部署讀回，不把會過期的 PID、container ID 或帳號資料寫進 current 文件。
