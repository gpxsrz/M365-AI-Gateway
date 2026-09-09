# 研究與驗證證據

## 30 秒看懂

> 白話：先問「我現在是在證明程式碼、測試、真實帳號、CI，還是 Production？」每一層只能證明自己的事，不能看到一個 PASS 就當全部都 PASS。這裡的 **evidence** 就是可回頭核對的證據，**identity** 就是它對應的確切版本／route／target。只要判斷證據層級，讀完本節和下表就停。

證據（evidence）分層：

| Evidence class | 能證明 | 不能自動推定 |
|---|---|---|
| Source / static trace | code path / contract目前長什麼樣 | runtime真的走到它 |
| Deterministic test | 固定輸入下contract成立 | Microsoft live一定一樣 |
| Local runtime | candidate artifact能啟動並走local seam | OAuth / upstream / Production已通過 |
| Isolated live | 特定account/route/time真實行為 | 永久支援或所有帳號相同 |
| Exact-head CI | published source在CI環境通過 | Production已部署 |
| Production readback | exact artifact真的在target runtime | 其他mirror也同步 |
| Inference | 目前evidence最合理的解釋 | 直接觀察事實 |

## 一筆可用evidence至少要回答

1. **Subject**：測哪個source / artifact / route / behavior？
2. **Identity**：commit / tree / binary / config / input / evidence SHA是什麼？
3. **Environment**：local、isolated live、CI還是Production？
4. **Expected**：contract要求什麼？
5. **Observed**：從target獨立readback看到什麼？
6. **Boundary**：哪些層沒有測？
7. **Privacy**：是否包含secret、private URL或可重播資料？若有就不能進public repo。

## Completion要分層

下列結果不能互相偷換：

```text
command exit 0
≠ request accepted
≠ upstream effect durable
≠ caller received
≠ semantic acceptance
≠ Production deployed
```

M365 transport可以證明自己的request、tool、checkpoint與delivery projection；ACP才判斷Task / Run semantic lifecycle。

## Current docs怎麼引用evidence

Current documentation只應寫穩定contract與目前有足夠evidence支持的結論。

不要在current page塞：

- 過期PID / container ID；
- private host/path；
- 單次帳號canary逐步紀錄；
- 已關Issue的整段timeline；
- 某個舊版本的temporary workaround。

這些資料若仍有調查價值，放到：

- [`../history/`](../history/README.md)；
- public Issue timeline；
- Git history；
- 授權的private evidence store。

## Evidence失效規則

任何controlling input改變，都只讓受影響的evidence stale：

- source / contract；
- test oracle / fixture；
- model mapping / capability evidence；
- integration plugin；
- binary / config；
- upstream/client版本；
- Production release unit。

不要因一個docs-only wording change就假裝runtime需要live canary；也不要因runtime曾經PASS就跳過新source identity需要的review / validation。

## 歷史與current分開

- **Current docs**：現在怎麼用、現在的contract。
- **History**：某個固定source / time發生過什麼。
- **Runtime readback**：現在target的實際狀態。
- **ACP authority**：Agent governance canonical state / decision。

遇到衝突，以當前canonical source / authority與exact readback為準；history降級成背景證據。

目前surface驗證狀態讀 [`compatibility.md`](compatibility.md)。
