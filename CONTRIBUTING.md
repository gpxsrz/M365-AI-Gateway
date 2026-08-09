# 貢獻說明

此倉庫是維護者整理的衍生參考快照，不是上游功能追蹤分支，也不承諾與每一個較新的上游變更保持同步。

## 原則

- 變更應先對應明確、可驗證的行為需求，再修改程式。
- 保持單一 Microsoft 365 帳號架構，不要重新引入多帳號路由、帳號池或投機性抽象。
- 不要提交 credentials、cookies、token cache、HAR、artifact bytes、私人測試資料或租戶識別資訊。
- 變更 Go 程式後請先執行 `gofmt` 與相關測試，再決定是否保留。
- 若你維護自己的衍生版本，請在自己的分支或 fork 上整理變更；此快照不作為上游協作入口，也不代轉上游 PR / issue。

## 建議驗證

至少執行：

```bash
gofmt -w <changed-go-files>
go test ./...
go mod verify
git diff --check
```

必要時再補 `go build ./...`、更聚焦的套件測試，或實際的本機管理介面驗證。
