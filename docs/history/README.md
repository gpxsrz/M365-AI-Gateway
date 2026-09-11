# 歷史證據 / Historical evidence

## 30 秒看懂 / Understand it in 30 seconds

這裡是唯讀歷史，不是目前操作手冊。AI Agent 平常不要載入；只有 current 文件明確指向舊 regression、Issue、canary 或決策來源時，才開一份 archive。

This is read-only history, not the current operating guide. AI agents should open one archive only when a current page explicitly requires an old regression, Issue, canary, or decision source.

## Archive

- [`memory-provider-compatibility-issues-42-44.md`](memory-provider-compatibility-issues-42-44.md): Issues #42–#44 的 Memory Provider hardening、舊 canary 與 live-qualification 記錄。
- [`issues-99-101-acceptance-2026-09-11.md`](issues-99-101-acceptance-2026-09-11.md): Issues #99/#100/#101 的 bounded acceptance、疑點鑑識與復原安全性記錄。
- [`issue-99-continuation-candidate-2026-09-11.md`](issue-99-continuation-candidate-2026-09-11.md): #99 的 Hermes 大型 JSON 操作驗證與 M365 continuation 修復候選；未部署、未結案。

Archive 內的 PASS 只適用於當時固定的 source、帳號、route 與 runtime，不能直接當成現在 Rust 或 Production 的 PASS。

Archived PASS results apply only to their pinned source, account, route, and runtime. They cannot be inherited by current Rust or Production.
