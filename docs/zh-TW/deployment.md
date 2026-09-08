# 部署與回復

## 30 秒看懂

> AI Agent：只讀本節就能判斷能不能部署。真的要操作時，再依序讀「安全部署順序」與完成表；私人主機細節不在本頁。

部署不是只換一個執行檔。請把 Rust 執行檔和三個管理網頁視為同一包；它們要來自同一個公開 commit，也要能一起回復。M365 durable transport checkpoint 不是 release artifact，但 rollback 必須恢復舊 binary 會讀到的 exact predeploy checkpoint state。

只有這些條件都成立，才算部署完成：

1. GitHub `main` 已讀回正確 commit，該 commit 的 CI 全綠。
2. 上線前已保存完整舊版本，也已在 quiesce 後保存 rollback 需要的 M365 checkpoint state。
3. 上線後的檔案、服務狀態與健康檢查都正確。
4. Hermes、Hindsight 或其他未授權服務沒有被改動。

本頁只放可公開重現的原則。NAS 主機名、Production 路徑、帳密與實際操作步驟不進 repo；維運時使用本機 `m365-ops` skill。

## 要部署哪些檔案

目前的完整 runtime set 是：

```text
m365-native
web/index.html
web/login.html
web/debug.html
```

Rust 也會把網頁內容編進 binary，Docker image 仍會帶上 `web/`。部署工具必須以同一個 commit 建出整包內容，不能把不同版本混在一起。

Data directory 另外可能有 private recovery pair：`transport-checkpoints.json` 與 `.transport-checkpoints.json.key`。它們**不是**公開 release archive 的內容。部署 helper 只有在服務停止後，才 snapshot 兩者的 exact presence 與 bytes；若 predeploy 原本不存在、candidate 後來建立，rollback 會恢復成「不存在」。這組檔案只是 M365 adapter recovery state；restore 不會 rewind ACP authority、Hermes state 或其他外部 effect。

Rollback 本身也必須先 quiesce。Helper 必須先成功停止 candidate service，才可 restore runtime 或 checkpoint 檔；若 rollback 的 stop 失敗，就不 restore 任何檔案、不重新啟動服務，保留 private backup 並回報 incomplete rollback，交由人工 recovery。

## 安全部署順序

1. 固定 public `main` 的 exact commit 與 tree。
2. 等該 exact head 的 CI 成功。
3. 建立 candidate，記錄每個檔案的 SHA-256。
4. 先 snapshot static runtime／rollback 檔案並證明可回復。
5. 停止服務，再對 quiesced checkpoint JSON + integrity key 的 exact presence／bytes 做 snapshot。
6. 在同一個停止服務視窗切換 candidate。
7. 逐一讀回檔案 hash、服務 PID、restart count、listener 與 health probe。
8. 任一檢查失敗，就先停止 candidate，restore 舊 runtime 與 exact predeploy checkpoint state，再啟動並驗證舊服務。

NAS、VM、dirty worktree 或尚未公開的 commit 都不是部署權威。

## Repo 內的部署工具

`scripts/deploy-nas-production.sh` 會把上述四個 release 檔案打成可重現的 archive。Manifest 綁定 exact commit、tree 與各 release 檔案 SHA-256。遠端會先驗 archive、manifest 與 payload，才允許切換。Private temporary rollback set 另外包含 Compose、settings，以及 quiesced checkpoint JSON／integrity-key 的 presence；這些 private state bytes 不會進公開 release archive。

腳本只接受非互動式 `sudo -n`。以下任一情況都會停止，不會勉強部署：

- 少任何一個檔案；
- 來源是 symlink；
- archive、manifest 或 hash 不一致；
- 部署後讀回的檔案 identity 不一致。

## Timeout 怎麼排

一個請求可能先排隊，再等 Microsoft 回應。因此外層 timeout 必須比內層總等待時間長。

例如：

| 等待層 | 範例值 |
|---|---:|
| `interactiveQueueTimeoutSeconds` | 300 秒 |
| `chatTimeoutSeconds` | 1800 秒 |
| Hermes stale detector | 約 2200 秒 |
| Hermes request timeout | 約 2300 秒 |
| reverse proxy read/send timeout | 約 2400 秒 |

這些數字只示範先後關係，不是永久預設值。改任何一層後都要重算。`proxy_connect_timeout` 只管建立連線，不必跟長推理 timeout 一樣久。

`textInputLimitUTF16` 是文字大小限制，與 timeout 無關。

## 設定與 Container

不同設定欄位有不同優先來源，不能假設環境變數或 `settings.json` 永遠勝出。管理頁應顯示目前生效值與來源；標成 environment-controlled 的值不能被管理頁覆蓋。

Repo `Dockerfile` 同時放入 binary 與 `web/`。如果 Production 把外部目錄 bind-mount 到 `/app`，真正執行的是 mount 內的檔案；驗收時要查 mount，不能只看 image。

## 可機械檢查的完成表

| 檢查 | 必須看到 |
|---|---|
| 公開來源 | exact commit / tree 與 intended source 相同 |
| CI | exact-head 成功 |
| Candidate | artifact identity 已固定 |
| 回復 | snapshot 涵蓋 runtime／rollback 檔案，以及 quiesced checkpoint JSON + integrity-key 的 exact presence／bytes |
| Production | binary 與 Web identity 全部吻合 |
| 服務 | state、restart count、listener、health 正常 |
| 邊界 | 未授權 runtime identity 沒有漂移 |
