# Rust 與歷史 Go parity

## 30 秒看懂

> Current source tree是Rust-only。只有你需要回答「原Go行為是否有不同」時，才讀歷史Go commit；不要把Go source恢復回current build tree。

Rust是目前唯一release / container source。

歷史Go實作只是一個固定parity reference：

```text
f038c86e62c7390c442f30043715255576db4e19
```

它可以回答「當時Go怎麼做」，不能自動證明current Rust / Microsoft live / Production現在怎麼做。

## Parity不是逐行翻譯

要保留的是observable contract與安全invariant，例如：

- 一個Gateway對一個Microsoft 365帳號；
- Microsoft登入只有一份主要credential lifecycle；
- document / image / artifact data boundary分開；
- caller取消stream時不要讓upstream work無限留在背景；
- tool ID / arguments / checkpoint identity不能猜測重建；
- Private mode每個新ChatHub transport都帶必要disable-memory intent；
- protected upstream URL不能直接洩漏給caller；
- unknown external outcome不能盲目replay。

Rust可以用更安全或更清楚的implementation實現同一contract；不需要複製Go內部結構。

## 什麼時候才查歷史Go

只有這些情況值得打開固定historical commit：

1. current Rust行為和已知user-facing contract衝突；
2. upstream interaction缺少明確spec，需要確認舊產品行為；
3. migration regression需要判斷Rust是否漏掉原有安全邊界。

查到historical behavior後，仍要用current Rust test / runtime evidence重新證明，不把Go PASS直接繼承。

## Current Rust surface

| Surface | Current Rust contract |
|---|---|
| Chat Completions | non-stream / SSE、tools、usage、input policy、checkpoint |
| Responses | Responses request/continuation projection |
| Anthropic | Messages / tools / media projection |
| Hermes | execution provenance、transport ledger、checkpoint / replay safety |
| Hindsight | Memory queue、overflow、webhook、durability barrier |
| OAuth | single-account credential lifecycle |
| Files / Vision | validated transport與grounding |
| Code Interpreter | protected artifact materialization / local capability |
| MCP | modern HTTP與legacy compatibility boundary |
| Admin | bootstrap、API key、settings、privacy-safe diagnostics |
| Release | locked Rust build、release unit、rollback contract |

Task / Run governance不在這張表；它屬於standalone ACP。

## Release evidence

Current Rust candidate要依變更範圍取得適用evidence：

1. source / formatting / tests / clippy / release build；
2. architecture / contract regression；
3. independent review（若controlling behavior改變）；
4. publication / exact-head CI（若本輪發布）；
5. artifact / Production readback（若本輪部署）；
6. live provider check只在accepted scope需要時執行。

Local、CI、live與Production是不同evidence layers。

## 不要把migration history留在current page

過去Rust rewrite曾抓到哪些bug、哪次canary失敗、哪個舊binary在Production，都應留在Git/history evidence，不再當current usage guide的一部分。

Current capability讀 [`compatibility.md`](compatibility.md)，歷史入口讀 [`../history/README.md`](../history/README.md)。
