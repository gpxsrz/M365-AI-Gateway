# 安全性說明

此公開快照只適用於你有權使用的 Microsoft 365 帳號與租戶環境。

## 最低限度安全原則

- 預設只綁定在 `127.0.0.1`；若要對外暴露，請自行補上可信的認證、TLS、反向代理與網路邊界控管。
- 不要在任何公開紀錄中提交 token、cookies、HAR、OneDrive / SharePoint 私有連結、artifact 原始位址或帳號快取。
- `Private / Temporary Chat` 只代表 no-ordinary-history transport policy，不代表 Microsoft 完全不保留任何資料；一般文件上傳仍可能建立 OneDrive / SharePoint staging copy。
- `Code Interpreter` 產生的 artifact 若要回傳給 consumer，必須走 authenticated artifact fetch；不要直接暴露瀏覽器 `blob:` URL 或未驗證的上游暫時連結。

## 回報方式

若你在這個衍生快照中發現安全問題，請直接私下聯絡目前維護者，附上最小重現資訊、影響範圍與版本描述，並先自行遮蔽所有敏感資料。

請不要把私密憑證、個資或可重放的封包直接貼進公開議題、公開討論串或上游專案。
