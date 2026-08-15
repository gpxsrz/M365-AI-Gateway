# 部署、Reverse Proxy 與 runtime identity

這份文件只描述公開可重現的部署原則。私人 NAS hostname、路徑、credential 與正式 Production 操作 SOP 不放在 repo；實際維運使用本機 `m365-ops` skill。

## Source of truth

Deployment candidate 必須綁定公開 `main` 的 exact commit / tree，並先通過該 exact head 的 CI。NAS、VM、dirty worktree 或未公開 commit 都不能成為部署權威。

## 一個 Production deployment 是一組 runtime artifact

Runtime 不只包含 binary。Server 會從工作目錄讀取：

```text
m365-native
web/index.html
web/login.html
web/debug.html
```

因此 binary 與 Web assets 必須來自同一 intended commit，在同一部署視窗切換、同一 rollback set 還原，並在部署後逐一做 identity readback。

### Release-unit automation

`scripts/deploy-nas-production.sh` 將 binary 與固定三個 Web assets 打包成 deterministic release archive；manifest 綁定 exact commit、tree 與四個檔案的 SHA-256。部署端會先驗 archive／manifest／payload identity，再把四個 runtime 檔案納入同一 snapshot、同一停機視窗切換、同一 rollback，最後逐一讀回 SHA。

腳本採 non-interactive `sudo -n` privilege path，不接受 password-fed `sudo -S`。缺任一 Web asset、來源是 symlink、archive/hash/manifest 不一致或部署後 identity 不符時都 fail closed。

## Snapshot / rollback 原則

部署前 snapshot 必須涵蓋本次會切換或 mutation 的完整 runtime set。部署失敗時 rollback 也要還原同一組 artifact；只還 binary 而留下新／舊 Web 混搭不是完整 rollback。

## Timeout stack

`chatTimeoutSeconds` 只控制 request 進入 ChatHub 後 Sidecar 等待時間；request 在 admission queue 可能另外等待 `interactiveQueueTimeoutSeconds`。外層 reverse proxy timeout 必須大於這些有效等待層級，否則會先被 proxy 終止。

Proxy timeout 與 `textInputLimitUTF16` 是兩種不同限制：前者是時間，後者是 caller text 的 UTF-16 policy。

例如 `interactiveQueueTimeoutSeconds=300`、`chatTimeoutSeconds=1800` 時，Sidecar 內層最長等待預算約為 `2100` 秒。此時可讓 Hermes stale detector 約 `2200` 秒、Hermes request timeout 約 `2300` 秒、reverse proxy `proxy_read_timeout` / `proxy_send_timeout` 約 `2400` 秒。這是**層級關係示例**，不是永久固定值；任一層修改後都要重新計算整條 timeout chain。`proxy_connect_timeout` 只控制建立到 Sidecar 的連線，不需要跟 reasoning timeout 一樣長。

## 設定來源

設定 precedence 依欄位類型而定，不應假設「環境變數永遠優先」或「settings.json 永遠優先」。管理 UI 應顯示 effective value 與 source；標示 environment-controlled 的欄位不能被 UI 保存值覆蓋。

## Container image

Repo `Dockerfile` 會把 binary 與 `web/` 一起放入 image。若 Production 額外 bind-mount `/app`，外部 `/app` 會成為實際 runtime source，因此部署 gate 必須驗證 mount 上的 binary 與 Web assets，而不能只相信 image 內建內容。

## 完成條件

Deployment 完成至少需要：

1. public exact commit / tree 已讀回；
2. exact-head CI success；
3. candidate artifact identity 固定；
4. snapshot / rollback set 已驗證；
5. Production binary + Web identity 等於 intended source；
6. service state / restart count / health probe 正常；
7. 未授權的 Hermes / Hindsight /其他 runtime identity 沒漂移。

真正的 Production mutation 細節不在本文件，請使用私人 operations skill。
