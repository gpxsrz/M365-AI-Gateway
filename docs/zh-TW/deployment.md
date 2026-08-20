# 部署與回復

## 30 秒看懂

部署不是只換一個執行檔。請把 Rust 執行檔和三個管理網頁視為同一包；它們要來自同一個公開 commit，也要能一起回復。

只有這些條件都成立，才算部署完成：

1. GitHub `main` 已讀回正確 commit，該 commit 的 CI 全綠。
2. 上線前已保存完整舊版本，失敗時可以整包回復。
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

## 安全部署順序

1. 固定 public `main` 的 exact commit 與 tree。
2. 等該 exact head 的 CI 成功。
3. 建立 candidate，記錄每個檔案的 SHA-256。
4. 對目前整組 runtime 做 snapshot，先證明可回復。
5. 在同一個停止服務的視窗切換整組檔案。
6. 逐一讀回檔案 hash、服務 PID、restart count、listener 與 health probe。
7. 任一檢查失敗，就回復整組舊檔案，再次驗證服務。

NAS、VM、dirty worktree 或尚未公開的 commit 都不是部署權威。

## Repo 內的部署工具

`scripts/deploy-nas-production.sh` 會把上述四個檔案打成可重現的 release archive。Manifest 綁定 exact commit、tree 與各檔案 SHA-256。遠端會先驗 archive、manifest 與 payload，才允許切換。

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
| 回復 | snapshot 涵蓋完整 runtime set |
| Production | binary 與 Web identity 全部吻合 |
| 服務 | state、restart count、listener、health 正常 |
| 邊界 | 未授權 runtime identity 沒有漂移 |
