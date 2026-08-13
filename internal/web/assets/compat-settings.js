(() => {
  "use strict";

  const extensionRow = "compat-admission-setting";
  const text = {
    zh: {
      maxConcurrent: "互動流量同時請求上限",
      queueTimeout: "互動流量排隊逾時（秒）",
      maxHelp: "限制同一 Microsoft 365 帳號同時進入 ChatHub 的互動式請求數；包含一般 /v1、Hermes、Responses 與 Anthropic。",
      queueHelp: "互動流量等待帳號容量的最長時間；逾時會回傳可重試的 503 與 Retry-After。",
      chatTimeoutHelp: "Sidecar 等待 ChatHub 聊天完成的最長秒數；完整 interactive request budget 還要加上排隊逾時。外層 proxy 與 caller timeout 必須大於兩者總和。",
      interactiveWaiting: "互動流量排隊中",
      shared429: "共享 429 次數",
      sharedCooldown: "共享冷卻至",
      last429Source: "最後 429 來源",
      checkpoint: "Checkpoint 持久化",
      checkpointRecords: "Checkpoint 筆數",
      generationSwitches: "Generation 切換次數",
      reusedWritten: "最近重用 / 寫入",
      lastSwitch: "最近切換耗時",
      none: "無",
      interactive: "互動流量",
      memory: "Memory",
    },
    en: {
      maxConcurrent: "Interactive Max Concurrent Requests",
      queueTimeout: "Interactive Queue Timeout (seconds)",
      maxHelp: "Limits interactive requests entering ChatHub for one Microsoft 365 account, including generic /v1, Hermes, Responses, and Anthropic traffic.",
      queueHelp: "Maximum time interactive traffic waits for account capacity; timeout returns a retryable 503 with Retry-After.",
      chatTimeoutHelp: "Maximum time the Sidecar waits for ChatHub after admission. The full interactive request budget also includes queue timeout; outer proxy and caller timeouts must exceed their sum.",
      interactiveWaiting: "Interactive waiting",
      shared429: "Shared 429 count",
      sharedCooldown: "Shared cooldown until",
      last429Source: "Latest 429 source",
      checkpoint: "Checkpoint persistence",
      checkpointRecords: "Checkpoint records",
      generationSwitches: "Generation switches",
      reusedWritten: "Last reused / written",
      lastSwitch: "Last switch duration",
      none: "None",
      interactive: "Interactive",
      memory: "Memory",
    },
  };

  function copy() {
    return document.documentElement.lang === "en" ? text.en : text.zh;
  }

  function installTranslations() {
    if (typeof translations !== "object" || translations === null) return;
    Object.assign(translations, {
      "互動流量同時請求上限必須為 1-16": "Interactive max concurrent requests must be between 1 and 16",
      "互動流量排隊逾時必須為 1-600 秒": "Interactive queue timeout must be between 1 and 600 seconds",
      "Sidecar 等待 ChatHub 聊天完成的最長秒數；完整 interactive request budget 還要加上排隊逾時。外層 proxy 與 caller timeout 必須大於兩者總和。": "Maximum time the Sidecar waits for ChatHub after admission. The full interactive request budget also includes queue timeout; outer proxy and caller timeouts must exceed their sum.",
    });
  }

  function installSectionStyle() {
    if (document.getElementById("compatAdmissionSectionStyle")) return;
    const style = document.createElement("style");
    style.id = "compatAdmissionSectionStyle";
    style.textContent = `
      #settingsForm .form-row:nth-child(9),
      #settingsForm .form-row:nth-child(10),
      #settingsForm .form-row:nth-child(18),
      #settingsForm .form-row:nth-child(25) {
        grid-column:auto;border-top:0;padding-top:0;margin-top:0
      }
      #settingsForm .form-row:nth-child(9)::before,
      #settingsForm .form-row:nth-child(10)::before,
      #settingsForm .form-row:nth-child(18)::before,
      #settingsForm .form-row:nth-child(25)::before {content:none!important;display:none!important}
      #settingsForm .form-row:nth-child(11),
      #settingsForm .form-row:nth-child(12),
      #settingsForm .form-row:nth-child(20),
      #settingsForm .form-row:nth-child(27) {
        grid-column:1/-1;border-top:1px solid var(--line);padding-top:16px;margin-top:2px
      }
      #settingsForm .form-row:nth-child(11)::before,
      #settingsForm .form-row:nth-child(12)::before,
      #settingsForm .form-row:nth-child(20)::before,
      #settingsForm .form-row:nth-child(27)::before {
        display:block;color:var(--text);font-size:12px;font-weight:700;letter-spacing:.04em;margin-bottom:12px;text-transform:uppercase
      }
      #settingsForm .form-row:nth-child(11)::before {content:"工具與模型"}
      #settingsForm .form-row:nth-child(12)::before {content:"限制與執行"}
      #settingsForm .form-row:nth-child(20)::before {content:"重新啟動與路徑"}
      #settingsForm .form-row:nth-child(27)::before {content:"OAuth"}
      html[lang="en"] #settingsForm .form-row:nth-child(11)::before {content:"Tools & Models"}
      html[lang="en"] #settingsForm .form-row:nth-child(12)::before {content:"Limits & Runtime"}
      html[lang="en"] #settingsForm .form-row:nth-child(20)::before {content:"Restart & Paths"}
      html[lang="en"] #settingsForm .form-row:nth-child(27)::before {content:"OAuth"}
    `;
    document.head.appendChild(style);
  }

  function settingRow(key, label, help, value, status) {
    const row = document.createElement("div");
    row.className = `form-row ${extensionRow}`;
    row.innerHTML = `<label>${esc(label)}</label><input class="input setting-input" data-key="${esc(key)}" type="number" value="${esc(value ?? "")}"><div class="help">${esc(help)}</div>${typeof settingStatusHelp === "function" ? settingStatusHelp(status) : ""}`;
    return row;
  }

  function sourceName(source) {
    const labels = copy();
    if (source === "interactive") return labels.interactive;
    if (source === "memory") return labels.memory;
    return source || labels.none;
  }

  function appendDiagnostics(data) {
    const panel = document.getElementById("compatTraffic");
    if (!panel) return;
    panel.querySelector("#compatAdmissionDiagnostics")?.remove();
    const labels = copy();
    const traffic = data.compatibilityTraffic || {};
    const checkpoint = data.checkpointPersistence || {};
    const cooldown = traffic.sharedCooldownUntil || traffic.memoryCooldownUntil;
    const diagnostics = document.createElement("div");
    diagnostics.id = "compatAdmissionDiagnostics";
    diagnostics.innerHTML = `<div class="help" style="margin-top:6px">${esc(labels.interactiveWaiting)}: ${Number(traffic.interactiveWaiting || 0)} · ${esc(labels.shared429)}: ${Number(traffic.shared429Count || 0)} · ${esc(labels.last429Source)}: ${esc(sourceName(traffic.last429Source))}${cooldown ? ` · ${esc(labels.sharedCooldown)}: ${esc(formatTaipeiTime(cooldown))}` : ""}</div><div class="help" style="margin-top:6px"><b>${esc(labels.checkpoint)}</b> · ${esc(labels.checkpointRecords)}: ${Number(checkpoint.recordCount || 0)} · ${esc(labels.generationSwitches)}: ${Number(checkpoint.generationSwitchCount || 0)} · ${esc(labels.reusedWritten)}: ${Number(checkpoint.lastGenerationReusedRecordCount || 0)} / ${Number(checkpoint.lastGenerationWrittenRecordCount || 0)} · ${esc(labels.lastSwitch)}: ${Number(checkpoint.lastGenerationDurationMs || 0)} ms</div>`;
    panel.appendChild(diagnostics);
  }

  async function renderCompatibilityAdmissionSettings(data) {
    const form = document.getElementById("settingsForm");
    if (!form) return;
    form.querySelectorAll(`.${extensionRow}`).forEach((row) => row.remove());
    const settings = data.settings || {};
    const status = data.settingStatus || {};
    const labels = copy();
    const anchor = form.querySelector('[data-key="memoryCompatibilityEnabled"]')?.closest(".form-row");
    if (!anchor) return;
    const maxRow = settingRow("interactiveMaxConcurrent", labels.maxConcurrent, labels.maxHelp, settings.interactiveMaxConcurrent, status.interactiveMaxConcurrent);
    const timeoutRow = settingRow("interactiveQueueTimeoutSeconds", labels.queueTimeout, labels.queueHelp, settings.interactiveQueueTimeoutSeconds, status.interactiveQueueTimeoutSeconds);
    anchor.after(maxRow, timeoutRow);
    const chatTimeoutHelp = form.querySelector('[data-key="chatTimeoutSeconds"]')?.closest(".form-row")?.querySelector(".help");
    if (chatTimeoutHelp) chatTimeoutHelp.textContent = text.zh.chatTimeoutHelp;
    appendDiagnostics(data);
  }

  async function refreshCompatibilityAdmissionSettings() {
    try {
      const data = await api("/api/admin/settings");
      await renderCompatibilityAdmissionSettings(data);
      applyLanguage();
    } catch (error) {
      note(error.message);
    }
  }

  installTranslations();
  installSectionStyle();
  const originalLoadSettings = window.loadSettings;
  if (typeof originalLoadSettings === "function") {
    window.loadSettings = async function loadSettingsWithCompatibilityAdmission() {
      const result = await originalLoadSettings();
      await refreshCompatibilityAdmissionSettings();
      return result;
    };
  }
  window.addEventListener("DOMContentLoaded", () => {
    if (document.getElementById("page-settings")?.style.display !== "none") {
      window.loadSettings?.();
    }
  });
})();
