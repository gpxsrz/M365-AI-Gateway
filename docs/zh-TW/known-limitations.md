# 已知限制

## 30 秒看懂

> 這頁只列「現在仍成立的限制」。舊版本bug、已關Issue、過去canary或一次性workaround請去history，不放回current page。

先記住六件事：

1. Microsoft capability會依帳號、rollout與時間改變。
2. Private mode不代表Microsoft零保留。
3. Attachment grounding不是任意byte-addressable storage，也不是零context cost。
4. 一個Gateway只有一個Microsoft帳號，throughput刻意受限。
5. Unknown upstream outcome會fence replay，可能需要人工reconcile。
6. M365不提供Task / Run semantic completion authority。

## Input 與 context

- M365 configured UTF-16 transport text limit 不是 model token ceiling；精確 current value 只在 [`runtime-settings.md`](runtime-settings.md) 維護。
- 非Memory bulk text只有在可以保留current ask、control與tool identity時才會auto-spill；沒有安全candidate就fail closed。
- Bulk spill仍無法容納時，full-context TXT fallback只承載目前 request 的 model-facing projection；它不是 session history、memory store 或 Task / Run authority，也不代表模型已完整讀取或正確使用文件。
- Memory route不auto-spill，input太長時要求consumer compact / split。
- Large attachment被Microsoft grounding後，Gateway不能保證任意高熵byte位置都能精確retrieval。

## Tools 與 continuation

- Parallel tools只在所有可選tool都明確read-only時開放；不能只看tool名稱猜測。
- Tool round有bounded safety ceiling，不是無限agent loop。
- 已送出的unknown transport outcome不能盲目retry；要先靠checkpoint / durable evidence判斷。
- Hermes duplicate-effect protection只保護transport effect，不證明Task acceptance。
- 若一次 bounded continuation 遇到被安全檢查拒絕的重播，Gateway只阻止該候選；保留caller原本的工具契約，讓不同且可解析的下一個tool call繼續。重播再次出現且沒有合法下一步時，非串流回 typed HTTP `409 unsafe_tool_replay`，串流送出同一錯誤代碼後結束；若原本是 `required` 或特定工具選擇而沒有合法 call，則回 `tool_choice_unsatisfied`。兩者都不產生成功checkpoint。
- Hermes的大型單行JSON即使已完整存成 persisted output，原生`read_file`仍可能受`max_line_length`截斷，且零換行單行的`total_lines`／`truncated` metadata不能單獨證明完整。應使用既有 terminal或`execute_code`在原檔解析，只輸出指定欄位或有限字元範圍；這不是 M365 full-context TXT spill。

## Microsoft surface variability

- Model selector、reasoning route、image resource與其他Web capability可能隨Microsoft rollout改變。
- Web observation只會成為capability candidate，沒有evidence / validation不應自動啟用。
- 一次`no_image_resource`或一次live success只描述當時account/route，不是永久產品保證。

## Privacy 與 files

- Private mode只處理一般chat-history intent；文件、圖片、artifact各有自己的資料生命週期。
- Protected artifact capability本身就是短效下載權限，洩漏後不能當一般網址看待。
- Gateway 會保護文件與 Code Interpreter artifact 的 upstream private URL；圖片生成的 `url` response 可能仍是 upstream image URL，應視為敏感、短期資源。Gateway 也無法替 Microsoft 做「零 retention」承諾。

## External clients

- OpenAI / Anthropic / MCP compatibility鎖住的是公開contract，不代表所有SDK每一個版本都已真實跑過。
- Legacy MCP SSE保留相容入口；新client優先使用modern MCP HTTP surface。
- Caller timeout、proxy timeout與upstream wait要一起配置；任一外層過短都可能讓合法長request被caller先中止。

## Shared account

Shared-account transport有意限制in-flight與queue，避免Hermes、Hindsight與foreground caller一起把同一Microsoft帳號壓爆。

這代表高吞吐不是本設計目標。要提高整體吞吐時，正確方向是獨立帳號／獨立Gateway，不是把單帳號hard safety limit硬拉高。

Memory request一旦已送到upstream不會被新user request強制取消；priority主要影響admission與waiting order。

## Memory freshness

`retain.completed`證明特定retain durable，不代表一筆先前已組好的HTTP body突然包含新memory。需要fresh memory時，要從下一次正常recall/readback證明。

## Governance boundary

M365可以提供transport evidence、checkpoint、provenance與typed result classification；它不能把模型說「完成」或tool exit 0升格成Task / Run canonical completion。

需要semantic lifecycle請使用standalone ACP。

目前驗證層級讀 [`compatibility.md`](compatibility.md)，歷史問題讀 [`../history/README.md`](../history/README.md)。
