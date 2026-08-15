package web

import "bytes"

const issue71UIAugmentationMarker = "m365-issue71-traffic-ui"

const issue71UIAugmentationScript = `<script id="m365-issue71-traffic-ui">
(function(){
  function txt(zh,en){ return uiLang==='en'?en:zh; }
  function legacyField(key,zh,en,helpZh,helpEn){
    const input=document.querySelector('[data-key="'+key+'"]');
    const row=input?.closest('.form-row');
    if(!row)return;
    const label=row.querySelector('label');
    if(label)label.childNodes[0].nodeValue=(uiLang==='en'?en:zh);
    if(!row.querySelector('[data-issue71-legacy]')){
      const help=document.createElement('div');
      help.className='help'; help.dataset.issue71Legacy='1';
      help.textContent=uiLang==='en'?helpEn:helpZh;
      row.appendChild(help);
    }
  }
  function when(v){ return v?formatTaipeiTime(v):'—'; }
  async function augmentIssue71Settings(){
    legacyField('memoryBackoffInitialSeconds','Memory 429 舊版初始退避（相容欄位）','Legacy Memory 429 Initial Backoff (compatibility field)','Issue #71 的 shared-account circuit breaker 不再使用此值；固定工程階梯為 1125 / 2250 / 4500 / 9000 / 18000 秒。','Issue #71 no longer uses this value for the shared-account circuit breaker; the engineering ladder is fixed at 1125 / 2250 / 4500 / 9000 / 18000 seconds.');
    legacyField('memoryBackoffMaxSeconds','Memory 429 舊版最大退避（相容欄位）','Legacy Memory 429 Maximum Backoff (compatibility field)','此欄位只保留 settings/API 相容；新的 shared breaker 最大 cooldown 固定為 18000 秒。','This field remains only for settings/API compatibility; the new shared breaker caps cooldown at 18000 seconds.');
    const box=document.getElementById('compatTraffic');
    if(!box)return;
    try{
      const d=await api('/api/admin/settings');
      const x=d.compatibilityTraffic||{};
      document.getElementById('issue71TrafficDetails')?.remove();
      const details=document.createElement('div'); details.id='issue71TrafficDetails';
      const yieldState=x.memoryYieldActive?'ACTIVE':(x.memoryYieldPending?'PENDING':'—');
      details.innerHTML='<div class="help" style="margin-top:8px"><b>'+esc(txt('Issue #71 流量模式','Issue #71 traffic mode'))+': '+esc(x.trafficMode||'NORMAL')+'</b> · '+esc(txt('Hermes 有效併發','Hermes effective concurrency'))+': '+Number(x.effectiveHermesConcurrency||0)+' · '+esc(txt('外部使用者執行中','External user in flight'))+': '+Number(x.externalUserInFlight||0)+' · '+esc(txt('自動作業執行/等待','Autonomous in flight/waiting'))+': '+Number(x.autonomousInFlight||0)+' / '+Number(x.autonomousWaiting||0)+'</div>'+
        '<div class="help" style="margin-top:6px">'+esc(txt('Memory pending / 執行 / 等待','Memory pending / in flight / waiting'))+': '+Number(x.memoryPendingCount||0)+' / '+Number(x.memoryInFlight||0)+' / '+Number(x.memoryWaiting||0)+' · '+esc(txt('最舊 Memory 年齡','Oldest Memory age'))+': '+Number(x.oldestMemoryAgeSeconds||0)+'s</div>'+
        '<div class="help" style="margin-top:6px">'+esc(txt('Milestone yield','Milestone yield'))+': '+esc(yieldState)+(x.memoryYieldDeadline?' · '+esc(txt('期限','deadline'))+': '+when(x.memoryYieldDeadline):'')+' · '+esc(txt('最近結果/耗時','last outcome/duration'))+': '+esc(x.lastMemoryYieldOutcome||'—')+' / '+Number(x.lastMemoryYieldDurationMs||0)+'ms · '+esc(txt('最近 retain','last retain'))+': '+when(x.lastSuccessfulRetain)+' · '+esc(txt('最近 consolidation','last consolidation'))+': '+when(x.lastSuccessfulConsolidation)+'</div>'+
        '<div class="help" style="margin-top:6px">'+esc(txt('最後 hard 429','last hard 429'))+': '+when(x.lastHard429)+' · '+esc(txt('最後 soft throttle','last soft throttle'))+': '+when(x.lastSoftThrottle)+' · '+esc(txt('throttle 連續/剩餘 cooldown','throttle streak/cooldown remaining'))+': '+Number(x.throttleStreak||0)+' / '+Number(x.sharedCooldownRemainingSeconds||0)+'s · '+esc(txt('已抑制 reask','reasks suppressed'))+': '+Number(x.reaskSuppressedCount||0)+'</div>';
      if(x.trafficMode==='RECOVERY'){
        const actions=document.createElement('div'); actions.className='actions'; actions.style.marginTop='8px';
        const button=document.createElement('button'); button.className='btn'; button.type='button'; button.textContent=txt('完成受控 Recovery','Complete controlled recovery');
        button.onclick=async()=>{ if(!confirm(txt('只有完成 live qualification 後才應關閉 RECOVERY。確定繼續？','Close RECOVERY only after live qualification. Continue?')))return; try{ await api('/api/admin/traffic/recovery',{method:'POST',body:JSON.stringify({action:'complete'})}); await loadSettings(); }catch(e){ note(e.message); } };
        actions.appendChild(button); details.appendChild(actions);
      }
      box.appendChild(details);
      translateTree(details);
    }catch(e){}
  }
  const originalLoadSettings=loadSettings;
  loadSettings=async function(){ await originalLoadSettings.apply(this,arguments); await augmentIssue71Settings(); };
  if(document.getElementById('page-settings')?.style.display!=='none')augmentIssue71Settings();
})();
</script>`

func augmentIssue71AdminIndex(raw []byte) []byte {
	if bytes.Contains(raw, []byte(issue71UIAugmentationMarker)) {
		return raw
	}
	marker := []byte("</body>")
	if !bytes.Contains(raw, marker) {
		return raw
	}
	return bytes.Replace(raw, marker, append([]byte(issue71UIAugmentationScript), marker...), 1)
}
