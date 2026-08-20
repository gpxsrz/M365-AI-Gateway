# Rust 改寫完成度

## 30 秒看懂

Rust 已完成 Go `f038c86e62c7390c442f30043715255576db4e19` 的離線契約搬移，也能以 release binary 在本機啟動。

這句話不包含：真實 Microsoft OAuth／ChatHub／檔案／圖片／artifact、第三方 MCP client、GitHub exact-head CI、container、NAS／VM 或 Production。那些要在固定 Rust commit 後分開驗收。

| 問題 | 現在答案 |
|---|---|
| Rust 是 release / container 唯一建置來源嗎？ | 是 |
| 保留 Go source 的用途？ | 只做 deterministic comparison |
| 本機測試與啟動？ | PASS |
| 已批准替換 Production？ | 尚未由本機 PASS 推定 |
| 可以刪掉 Go comparison source 嗎？ | 不可以，沒有這項授權 |

## 功能對照

| Surface | 狀態 | 已覆蓋重點 |
|---|---|---|
| OpenAI Chat Completions | 本機 PASS | non-stream/SSE、structured output、tools、usage、`[DONE]` |
| Responses | 本機 PASS | parent continuation、tool result、parallel calls、reasoning/media events |
| Anthropic Messages | 本機 PASS | error、tool/image round trip、posthoc stream 與 ignored-parameter headers |
| Hermes | 本機 PASS | provenance、execution ledger、completion guard、多輪 tools、排程 |
| Hindsight | 本機 PASS | retain/recall/reflect、breaker、`MEMORY_YIELD`、webhook、barrier |
| MCP modern + legacy | 本機 PASS | session/Origin、tool list/call、legacy SSE/message boundary |
| Files / Vision | Offline seam PASS | magic/name/SSRF/quota/reuse/metadata；真實帳號待驗 |
| Images | Offline seam PASS | 會走 ChatHub transport 並投影結果；真實生成待驗 |
| Code Interpreter artifact | Offline seam PASS | 私有暫存、權限、path/network boundary、終點物化；真實 artifact 待驗 |
| Checkpoint continuation | 本機 PASS | history prefix、rollback-safe clear、parent、tool ledger、restart persistence |
| Caller tools | 本機 PASS | call identity、fail-closed limits、唯讀平行 allowlist、router/repair/final boundary |
| Streaming | 本機 PASS | frame 去重、usage、單一 `[DONE]`、error SSE、artifact URL holdback |
| Admin/settings/debug | 本機 PASS | bootstrap、API keys、partial update、env source、redaction、persistence |
| Legacy routes | Offline PASS | Go literal routes 與動態 Hindsight/artifact routes 已映射；另有 `/api/admin/traffic` |
| Model capability | 本機 PASS | built-in/configured/optional、evidence binding、observe-only drift |
| Release definition | 本機 PASS | pinned toolchain、locked build、Rust Dockerfile、六平台 matrix、checksum |

## 本機完成證據

```text
cargo fmt --all --check
cargo test --locked --all-targets       # 123 passed, 0 failed
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
go mod verify
go test ./...
go vet ./...
go build ./...
git diff --check
```

Release binary smoke 已完成：bootstrap login → 改密碼 → 重新登入 → 建立 API key → 用該 key 讀 `/v1/models`，全部 HTTP 200；沒有授權的 models request 是 401。

Source 也經 Serena semantic review 與 Code Review Graph incremental review。Graph 提醒的 test gaps 已逐項對照同模組或 route regression。

## 尚未完成的外部 gate

1. Commit 並發布到 public `main`。
2. GitHub exact-head CI 與 container build。
3. NAS / VM exact commit 同步。
4. 隔離 Microsoft 帳號的 OAuth、ChatHub、file、image、artifact live qualification。
5. 真實 MCP client qualification。
6. 完整 rollback 證據與 Production promotion/readback。

每一項都要固定 commit、route、帳號／runtime scope，並遵守 secret 零輸出。後續若完成，必須更新本頁，不能讓舊的「尚未執行」留在 current docs。
