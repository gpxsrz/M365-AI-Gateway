# 部署與回復

## 30 秒看懂

> 這是 public deployment contract，不是某台 NAS 的操作手冊。真的要動 Production 時，使用本機 `m365-ops` 做 exact target preflight；private host、path、credential 不進 repo。

部署要把同一個 source identity 的 runtime當成一個 release unit，而不是只換一個 binary。

Current public release unit：

```text
m365-native
web/index.html
web/login.html
web/debug.html
```

Hermes Native Attachment Bridge 是同一 release 的 plugin/runtime wiring，不是 Hermes core 變更。Production 需要保留 `m365-recall-provenance`，再載入 `m365-native-attachments`；只配置以下環境變數名稱並由既有 secret/env 機制提供值：`M365_HERMES_RECALL_PROVENANCE_SECRET`、`M365_HERMES_PROVIDER`、`M365_HERMES_GATEWAY_BASE_URL`、`M365_HERMES_ATTACHMENT_ALLOWED_ROOTS`。Gateway base URL 必須是 HTTPS，allowed roots 必須限制在 Outlook KB 原始附件目錄；不要把值、credential 或 private path 放入 repo。

Transport checkpoint / integrity state 是 private durable runtime state，**不是**公開 release artifact。Runtime rollback 不得回寫它、較新的 settings 或業務資料。

## 可以部署前要先有什麼

至少要固定：

1. intended source commit / tree；
2. 對應 local validation；
3. publication target與expected-old；
4. exact-head CI / container build（如果該發布流程需要）；
5. candidate artifact identity；
6. runtime artifact rollback 計畫，以及保留 durable state 的讀取相容性檢查。

Local PASS、GitHub publication、CI、NAS copy、VM source與Production deployment是不同 gate，不能互相代替。

## 安全部署順序

通用順序：

1. Freeze source commit / tree。
2. 驗證該 source需要的 build/test gate。
3. 建立 candidate並記錄 release file SHA-256。
4. Read back現有 Production runtime與recovery baseline。
5. Quiesce 服務，保存 runtime rollback 檔案，記錄 durable-state identity，但不還原這些資料檔。
6. 在同一停止視窗切換 candidate release unit。
7. 啟動後讀回 binary / Web assets、service state、restart count、listener、health。
8. 任一必要 readback失敗，依已驗證 recovery plan rollback；不要只因 process能啟動就宣稱回復成功。

部署過程不應順便修改未授權的 Hermes、Hindsight、Semantica 或 ACP runtime。

## Transport checkpoint rollback

Data directory可能包含：

```text
transport-checkpoints.json
.transport-checkpoints.json.key
```

它們不是 release archive內容。

Binary rollback 只還原 runtime binary、Web assets 與 Compose；不還原也不刪除 checkpoint 檔案、settings 或業務資料，即使它們在部署後才建立或更新。

舊 binary能啟動不代表它能安全讀新 checkpoint schema；回滾前須確認讀取相容性。無法確認時保留 durable state，改走 forward repair。

若 rollback本身無法安全 quiesce candidate，應停止還原 runtime 檔案並保留 recovery material 給人工處理。

## Repo deployment helper

Repo 的 `scripts/deploy-nas-production.sh` 是可重現 release/deploy automation之一。它的 public contract是：

- 綁定 exact commit / tree；
- 將 release unit打包並驗證 manifest / SHA；
- remote在切換前驗證 payload；
- 使用非互動式 privilege path；
- candidate readback不一致時fail closed並走rollback contract。

Private NAS hostname、volume path、帳密與實際 Production command不屬於這頁。

## Container 與 bind mount

Docker image可以包含 binary與 `web/`，但 runtime bind mount可能覆蓋 image內檔案。

所以驗收要查「process真正執行/讀取哪一份 bytes」，不能只看 image tag或build成功。

## Timeout 關係

一筆 request可能經歷：

```text
queue wait
→ ChatHub / model wait
→ caller / Hermes timeout
→ reverse proxy timeout
```

外層 timeout必須大於它包住的內層最壞等待；不要從某次Production數字硬抄成永遠預設。Current effective queue / chat timeout請從管理設定讀回。

`textInputLimitUTF16` 是文字大小政策，和 timeout無關。

## 完成要讀回什麼

| Gate | 必須證明 |
|---|---|
| Source | intended commit / tree |
| Build | candidate artifact identity |
| Publication | exact public ref（若本輪有發布） |
| CI | exact candidate head（若本輪需要） |
| Recovery | predeploy rollback bytes/state可識別且可用 |
| Production | binary與Web bytes來自同一candidate |
| Service | state / restart / listener / health符合contract |
| Scope | 未授權runtime沒有被一起mutation |

只有本輪真正包含的gate才需要驗證；docs-only change不需要因此部署Production。

## 接著讀哪裡

- Runtime settings：[`runtime-settings.md`](runtime-settings.md)
- Compatibility / evidence：[`compatibility.md`](compatibility.md)、[`research-evidence.md`](research-evidence.md)
- Security：[`../../SECURITY.md`](../../SECURITY.md)
- Private Production操作：本機 `m365-ops`
