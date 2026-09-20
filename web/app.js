import {patchHTML, installInteractionGuards, interacting, releaseControl, releaseControls} from './dom-sync.js';
const app = document.querySelector('#app');

// Theme: explicit choice wins, otherwise follow the OS. Stored locally because the
// hub is a single-purpose appliance and the preference is per-browser.
const THEME_KEY='oah-theme';
function applyTheme(pref){
  const root=document.documentElement;
  if(pref==='light'||pref==='dark')root.setAttribute('data-theme',pref);
  else root.removeAttribute('data-theme');
}
function currentThemePref(){try{return localStorage.getItem(THEME_KEY)||'system';}catch{return 'system';}}
function themeIsLight(){
  const pref=currentThemePref();
  if(pref==='light')return true;
  if(pref==='dark')return false;
  return window.matchMedia&&window.matchMedia('(prefers-color-scheme: light)').matches;
}
function cycleTheme(){
  const next=themeIsLight()?'dark':'light';
  try{localStorage.setItem(THEME_KEY,next);}catch{}
  applyTheme(next);
}
applyTheme(currentThemePref());

const model = {
  publicInfo: null,
  state: null,
  locked: true,
  route: 'Dashboard',
  eventSource: null,
  toastTimer: null,
  localMixer: null,
  localAudio: null,
  joinBssid: '',
  diagnostics: null,
  update: null,
  btBusy: false,
  realtime: 'connecting',
  stateError: '',
  btVolumeMemory: {},
  pendingVolumes: {},
  minRevision: 0,
  mixerDirty: false,
  mixerEdit: 0,
  mixerError: '',
};

const routes = ['Dashboard', 'Devices', 'Network', 'Audio', 'System', 'Diagnostics'];
const esc = (s='') => String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const attr = esc;
const fmtRate = r => r ? `${(r/1000).toFixed(r % 1000 ? 1 : 0)} kHz` : '—';
const roleLabel = r => (/^in\d+$/.test(r) ? 'Input '+r.slice(2) : /^out\d+$/.test(r) ? 'Output' : 'Unassigned');
const asArray = v => Array.isArray(v) ? v : [];
const asObject = v => v && typeof v === 'object' && !Array.isArray(v) ? v : {};
const finite = (v, fallback=0) => Number.isFinite(Number(v)) ? Number(v) : fallback;

function normalizeDiagnostics(raw={}) {
  const d = asObject(raw);
  return {
    cpuPercent: finite(d.cpuPercent), memPercent: finite(d.memPercent), tempC: finite(d.tempC), xruns: finite(d.xruns),
    transports: asArray(d.transports).map(t=>({...asObject(t), addr:String(t?.addr||''), codec:String(t?.codec||''), state:String(t?.state||''), rate:finite(t?.rate), volume:finite(t?.volume), delay:finite(t?.delay)})),
    services: asArray(d.services).map(x=>({name:String(x?.name||'Unknown service'), state:String(x?.state||'unknown')})),
    logs: asArray(d.logs).map(String),
  };
}

const DELAY_STATES=['default','unsupported','pending','reported','rejected','mismatch'];
function normalizeDelay(raw={}){
  const d=asObject(raw);
  return {requestedMs:finite(d.requestedMs),state:DELAY_STATES.includes(d.state)?d.state:'pending',input:String(d.input||''),addr:String(d.addr||''),attempt:String(d.attempt||''),detail:String(d.detail||''),actualMs:finite(d.actualMs),actualKnown:Boolean(d.actualKnown),attemptedAt:String(d.attemptedAt||''),capable:Boolean(d.capable)};
}

function normalizeState(raw={}) {
  const x = asObject(raw), slots = asObject(x.slots), mixer = asObject(x.mixer), wifi = asObject(x.wifi), audio = asObject(x.audio), system = asObject(x.system), health = asObject(x.health), pairing = asObject(x.pairing);
  const cap = finite((raw&&raw.inputCapacity&&raw.inputCapacity.max)||2,2);
  const inputs = asArray(slots.inputs).slice(0,cap).map(v=>String(v||'')); while(inputs.length<cap) inputs.push('');
  const outputs = asArray(slots.outputs).slice(0,1).map(v=>String(v||'')); while(outputs.length<1) outputs.push('');
  const gains = asArray(mixer.gains).slice(0,cap).map(v=>finite(v)); while(gains.length<cap) gains.push(0);
  const mutes = asArray(mixer.mutes).slice(0,cap).map(Boolean); while(mutes.length<cap) mutes.push(false);
  const placement = asArray(mixer.placement).slice(0,cap).map(v=>['stereo','left','right'].includes(v)?v:'stereo'); while(placement.length<cap) placement.push('stereo');
  const devices = asArray(x.devices).map((v, index)=>{const d=asObject(v); return {
    ...d, reason:String(d.reason||''), sbcMaxBitpool:finite(d.sbcMaxBitpool), id:String(d.id||`device-${index}`), addr:String(d.addr||''), name:String(d.name||'Unnamed Bluetooth device'), kind:String(d.kind||'unknown'),
    caps:asArray(d.caps).map(String), paired:Boolean(d.paired), trusted:Boolean(d.trusted), connected:Boolean(d.connected), status:String(d.status||'disconnected'), role:String(d.role||''),
    autoConnect:Boolean(d.autoConnect), backend:String(d.backend||''), rssi:finite(d.rssi), rate:finite(d.rate), volume:finite(d.volume,100), volumeKnown:Boolean(d.volumeKnown), latencyMs:finite(d.latencyMs), codec:String(d.codec||'')
  }});
  const hWifi=asObject(health.wifi), hBt=asObject(health.bluetooth), hAudio=asObject(health.audio), hSystem=asObject(health.system);
  return {
    ...x, revision:finite(x.revision), devices,
    slots:{inputs,outputs},
    mixer:{gains,mutes,placement,master:finite(mixer.master),masterMute:Boolean(mixer.masterMute),headroomDb:finite(mixer.headroomDb,-6),limiter:Boolean(mixer.limiter)},
    wifi:{...wifi,interface:String(wifi.interface||'wlan0'),ssid:String(wifi.ssid||''),bssid:String(wifi.bssid||''),freq:finite(wifi.freq),band:String(wifi.band||'—'),channel:finite(wifi.channel),rssi:finite(wifi.rssi),ip:String(wifi.ip||''),networks:asArray(wifi.networks).map(n=>({...asObject(n),ssid:String(n?.ssid||''),bssid:String(n?.bssid||''),band:String(n?.band||'—'),freq:finite(n?.freq),channel:finite(n?.channel),rssi:finite(n?.rssi),secure:Boolean(n?.secure)})),apply:wifi.apply||null},
    audio:{...audio,secondarySbcMaxBitpool:finite(audio.secondarySbcMaxBitpool,35),secondaryAdvertisedDelayMs:finite(audio.secondaryAdvertisedDelayMs,0),preset:String(audio.preset||'balanced'),preferredRate:finite(audio.preferredRate,48000),allowedRates:asArray(audio.allowedRates).map(Number).filter(Number.isFinite),quantum:finite(audio.quantum,2048),bluealsaPeriodUs:finite(audio.bluealsaPeriodUs,100000),bluealsaBufferUs:finite(audio.bluealsaBufferUs,500000),resampler:String(audio.resampler||'auto'),codecPolicy:String(audio.codecPolicy||'compatibility'),liveMeters:Boolean(audio.liveMeters)},
    system:{...system,hostname:String(system.hostname||'openaudiohub'),mdns:String(system.mdns||'openaudiohub.local'),btName:String(system.btName||'OpenAudioHub'),version:String(system.version||'—'),os:String(system.os||'—'),time:String(system.time||new Date().toISOString()),uptime:String(system.uptime||'—')},
    health:{wifi:{state:String(hWifi.state||'error'),value:String(hWifi.value||'Unavailable'),warning:Boolean(hWifi.warning)},bluetooth:{state:String(hBt.state||'error'),value:String(hBt.value||'Unavailable'),activeSources:finite(hBt.activeSources),activeOutputs:finite(hBt.activeOutputs)},audio:{state:String(hAudio.state||'error'),value:String(hAudio.value||'Unavailable'),xruns:finite(hAudio.xruns)},system:{state:String(hSystem.state||'error'),value:String(hSystem.value||'Unavailable')}},
    pairing:{active:Boolean(pairing.active),until:String(pairing.until||''),scanning:Boolean(pairing.scanning)},
    diagnostics: normalizeDiagnostics(x.diagnostics),
    delayReport: normalizeDelay(x.delayReport),
    inputCapacity: (()=>{const c=asObject(x.inputCapacity);const max=Math.max(2,Math.min(8,finite(c.max,2)));return {max,proven:Math.max(1,Math.min(max,finite(c.proven,2)))};})(),
  };
}

function setState(raw){
  const next=normalizeState(raw);
  if(next.revision<model.minRevision)return;
  const previous=model.state;
  if(!model.mixerDirty)model.localMixer=structuredClone(next.mixer);
  if(!model.localAudio || (previous&&JSON.stringify(model.localAudio)===JSON.stringify(previous.audio)))model.localAudio=structuredClone(next.audio);
  for(const [addr,p] of Object.entries(model.pendingVolumes)) {
    const d=next.devices.find(d=>d.addr===addr);
    if(p.acknowledged&&d?.volumeKnown&&Math.abs(d.volume-p.value)<=1)delete model.pendingVolumes[addr];
    else if(p.acknowledged&&Date.now()>p.until) { delete model.pendingVolumes[addr];toast('The device did not confirm the new volume.','error'); }
  }
  model.state=next;model.stateError='';
}

async function api(path, options={}) {
  const opts = { credentials:'same-origin', ...options };
  if (opts.body && typeof opts.body !== 'string' && !(opts.body instanceof FormData)) {
    opts.headers = {'Content-Type':'application/json', ...(opts.headers||{})};
    opts.body = JSON.stringify(opts.body);
  }
  let res;
  try { res = await fetch(path, opts); }
  catch { throw new Error('OpenAudioHub is temporarily unreachable.'); }
  if (res.status === 401) {
    let msg='Incorrect password';
    try { const j=await res.json(); if(j?.error) msg=String(j.error); } catch {}
    if (path !== '/api/session') {
      msg='Your session expired. Sign in again.';
      model.locked=true; model.state=null; model.route='Dashboard'; closeEvents(); render();
    }
    throw new Error(msg);
  }
  if (!res.ok) {
    let msg = 'That request failed.';
    try { const j = await res.json(); msg = j?.error || msg; if(j?.reference) msg += ` (ref ${j.reference})`; } catch {}
    throw new Error(msg);
  }
  if (res.status === 204) return null;
  const ct = res.headers.get('content-type') || '';
  return ct.includes('json') ? res.json() : res.text();
}
// Serialize mutations and snapshot their bodies; an older write cannot finish
// after a newer one and put the mixer back at its previous value.
let writeQueue=Promise.resolve();
const post=(p,b)=>{
  const body=structuredClone(b);
  const job=writeQueue.then(()=>api(p,{method:'POST',body})).then(result=>{
    if(result?.revision!==undefined)model.minRevision=Math.max(model.minRevision,result.revision);
    return result;
  });
  writeQueue=job.catch(()=>{});return job;
};

function toast(text, kind='') {
  document.querySelector('.toast')?.remove();
  const el = document.createElement('div'); el.className = `toast ${kind}`; el.textContent = text; document.body.append(el);
  clearTimeout(model.toastTimer); model.toastTimer = setTimeout(()=>el.remove(),3500);
}

function logo(word=true) { return `<div class="brand"><img src="/assets/logo-mark.svg" alt="">${word?'<b>Open<span>Audio</span>Hub</b>':''}</div>`; }
function dot(state='disconnected'){return `<i class="dot ${attr(state)}"></i>`}
function pill(state,text){return `<span class="pill">${dot(state)}${esc(text)}</span>`}
function btn(text,action,kind='secondary',extra=''){return `<button class="btn ${kind}" data-action="${attr(action)}" ${extra}>${esc(text)}</button>`}
// Icon-only button for low-frequency or destructive row actions. The label stays
// in title/aria-label so the meaning is never icon-guessing.
function iconBtn(svg,label,action,kind='ghost',extra=''){return `<button class="btn icon-only ${kind}" data-action="${attr(action)}" title="${attr(label)}" aria-label="${attr(label)}" ${extra}>${svg}</button>`}
function signal(rssi=0){const n=!rssi?0:rssi>=-60?4:rssi>=-70?3:rssi>=-80?2:rssi?1:0;const lab=rssi?['','Weak','Fair','Good','Strong'][n]+' ('+rssi+' dBm)':'';return `<span class="signal${n<=1?' warn':''}" title="${attr(lab)}" aria-label="${attr(lab)}">${[1,2,3,4].map(i=>`<i class="${i<=n?'on':''}"></i>`).join('')}</span>`}
function card(inner,cls='',key=''){return `<div class="card ${cls}" ${key?`data-key="${attr(key)}"`:''}>${inner}</div>`}
// Quiet hint affordance: the explanation lives on hover/focus instead of as a
// paragraph under every control. Keeps the page scannable while the guidance
// stays one gesture away.
function hint(text){return `<span class="hint" tabindex="0" role="note" title="${attr(text)}" aria-label="${attr(text)}">${SVG.info}</span>`}

function section(title,inner,aside=''){return `<section class="section"><div class="section-head"><span>${esc(title)}</span>${aside}</div>${inner}</section>`}

// Icons and connector states follow the approved design language.
// They are presentation only: no control, role or data path depends on them.
const ICON={phone:'M7 2h10a2 2 0 012 2v16a2 2 0 01-2 2H7a2 2 0 01-2-2V4a2 2 0 012-2zM11 18h2',computer:'M2 4h20v12H2zM8 20h8',headphones:'M3 18v-6a9 9 0 0118 0v6M3 14h3v6H3zM18 14h3v6h-3z',speaker:'M6 2h12v20H6zM12 8h.01M12 15m-3 0a3 3 0 106 0 3 3 0 10-6 0',unknown:'M12 22a10 10 0 100-20 10 10 0 000 20zM9.5 9a2.5 2.5 0 015 0c0 2-2.5 2-2.5 4M12 17h.01'};
const SVG={speaker:'<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2 6h3l4-3v10l-4-3H2z"></path><path d="M11 6a3 3 0 010 4"></path></svg>',speakerOff:'<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2 6h3l4-3v10l-4-3H2z"></path><path d="M10.5 6.5l3 3M13.5 6.5l-3 3"></path></svg>',device:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7 2h10a2 2 0 012 2v16a2 2 0 01-2 2H7a2 2 0 01-2-2V4a2 2 0 012-2zM11 18h2"></path></svg>',warning:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 9v4M12 17h.01M10.3 3.9L1.8 18.6A2 2 0 003.5 21.6h17a2 2 0 001.7-3L13.7 3.9a2 2 0 00-3.4 0z"></path></svg>',logout:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 21H5a2 2 0 01-2-2V5a2 2 0 012-2h4M16 17l5-5-5-5M21 12H9"></path></svg>',trash:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14"></path></svg>',refresh:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 10-2.3 5.7M20 5v6h-6"></path></svg>',settings:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h10M18 7h2M4 17h4M12 17h8"></path><circle cx="16" cy="7" r="2"></circle><circle cx="10" cy="17" r="2"></circle></svg>',search:'<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="7"></circle><path d="M20 20l-3.5-3.5"></path></svg>',copy:'<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"></rect><path d="M5 15V6a2 2 0 012-2h9"></path></svg>',sun:'<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"></circle><path d="M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5L19 19M19 5l-1.5 1.5M6.5 17.5L5 19"></path></svg>',moon:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 14.5A8.5 8.5 0 019.5 4a8.5 8.5 0 1010.5 10.5z"></path></svg>',undo:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 8h11a5 5 0 010 10H8M4 8l4-4M4 8l4 4"></path></svg>',power:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 4v8M7.5 7a7 7 0 108.9 0"></path></svg>',info:'<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"></circle><path d="M12 11v5M12 8h.01"></path></svg>'};
function iconFor(d){const ic=String(d?.icon||'').toLowerCase(),caps=asArray(d?.caps),kind=String(d?.kind||'');let k='unknown';
  if(/headset|headphone/.test(ic))k='headphones';
  else if(/speaker|audio-card/.test(ic))k='speaker';
  else if(/phone/.test(ic))k='phone';
  else if(/computer|laptop/.test(ic))k='computer';
  else if(caps.includes('plays_audio')||kind==='output')k='speaker';
  else if(caps.includes('sends_audio')||kind==='source'||kind==='audio-bidirectional')k='phone';
  return `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="${ICON[k]}"></path></svg>`;}
function connClass(d){if(!d||!d.addr)return 'idle';return d.connected?'active':d.status==='connecting'?'connecting':'idle'}

let deferredRender=false;
installInteractionGuards(app,()=>{if(deferredRender&&!interacting(app)){deferredRender=false;render();}});
// Card geometry changes without a re-render when a disclosure opens or the
// window resizes, so re-attach the connector curves then too.
app.addEventListener('toggle',queueAlignConnectors,true);
window.addEventListener('resize',queueAlignConnectors);
function render(background=false) {
  if(background&&interacting(app)){deferredRender=true;return;}
  try {
    if (!model.publicInfo) { app.innerHTML = `<div class="loading">${logo()}<span>Starting…</span></div>`; return; }
    if (model.locked) { renderLock(); return; }
    if (!model.state) { app.innerHTML = `<div class="loading">${logo()}<span>${esc(model.stateError||'Loading hub state…')}</span>${model.stateError?btn('Retry','ui-retry','primary'):''}</div>`; return; }
    renderShell();
  } catch (err) {
    console.error('OpenAudioHub render error', err);
    app.innerHTML = `<div class="lock"><div class="lock-box">${logo()}${card(`<b>Could not display the current state</b><p>OpenAudioHub sent data this screen could not render. Your settings were not changed.</p>${btn('Reload state','ui-retry','primary')} ${btn('Dashboard','ui-dashboard','secondary')}`)}</div></div>`;
  }
}

function renderLock() {
  app.innerHTML = `<div class="lock"><form class="lock-box" id="login-form">
    <div class="lock-brand">${logo()}<span>${esc(model.publicInfo.mdns || 'openaudiohub.local')}</span></div>
    ${card(`<label>Password<input id="login-password" autofocus type="password" autocomplete="current-password" placeholder="Hub password"></label>
    <div id="login-error" class="form-error" hidden>${dot('error')}<span></span></div>
    <label class="check"><input id="login-remember" type="checkbox" checked>Stay signed in on this device</label>
    <button class="btn primary" type="submit">Unlock</button>`)}
  </form></div>`;
}

function renderShell() {
  const s = model.state;
  const page = ({Dashboard:dashboardPage,Devices:devicesPage,Network:networkPage,Audio:audioPage,System:systemPage,Diagnostics:diagnosticsPage})[model.route]();
  patchHTML(app, `<div class="app-shell">
    <header class="topbar">${logo()}<nav>${routes.map(r=>`<button data-route="${r}" class="${r===model.route?'active':''}">${r}</button>`).join('')}</nav>
      <div class="top-status">${dot(model.realtime==='connected'?'connected':'warning')}<span>${esc(s.system.hostname)}</span></div><button class="btn ghost icon-only" data-action="theme-toggle" title="Switch theme" aria-label="Switch theme">${themeIsLight()?SVG.moon:SVG.sun}</button>${iconBtn(SVG.logout,'Sign out','logout')}</header>
    <main data-key="page-${model.route}">${page}</main>
    <nav class="bottom-tabs">${routes.map(r=>`<button data-route="${r}" class="${r===model.route?'active':''}">${r}</button>`).join('')}</nav>
  </div>`, `shell-${model.route}`);
  alignConnectors();
}

// One status line, not three widgets. The category is evident from the reading,
// so it lives in the tooltip and the accessible name rather than as a third piece
// of text inside every box.
function healthStrip(){const h=model.state.health;
  const item=(route,kind,value,state,extra='')=>`<button data-route="${route}" title="${attr(kind+': '+value+(extra?' · '+extra:''))}" aria-label="${attr(kind+': '+value)}">${dot(state)}<span>${esc(value)}</span></button>`;
  return `<div class="health-strip">${
    item('Network','Wi-Fi',h.wifi.value,h.wifi.state,h.wifi.warning?'2.4 GHz competes with Bluetooth':'')
  }${item('Devices','Bluetooth',h.bluetooth.value,h.bluetooth.state)}${item('Audio','Audio',h.audio.value,h.audio.state)}</div>`}
function getDevice(addr){const a=String(addr||'').toUpperCase();return model.state.devices.find(d=>String(d.addr||'').toUpperCase()===a)}
function assignedAction(d){
  if(!d)return '';
  const act=d.connected?'disconnect':'connect';
  const label=d.connected?'Disconnect':d.status==='connecting'?'Cancel':d.status==='error'?'Retry':'Connect';
  return btn(label,`bt-action:${act}:${d.addr}`,'secondary',model.btBusy?'disabled':'');
}
function autoToggle(d){
  if(!d)return '';
  return `<button class="auto-toggle ${d.autoConnect?'on':''}" data-bt-auto="${attr(d.addr)}" data-auto-enabled="${d.autoConnect?'1':'0'}" title="Reconnect this device automatically" aria-label="Reconnect this device automatically" aria-pressed="${d.autoConnect?'true':'false'}" ${model.btBusy?'disabled':''}><span>Auto</span><i><b></b></i></button>`;
}
function nodeCard(label,d,index,m,output=false){
  if(!d)return card(`<small class="micro-label">${label}</small><span class="empty-icon">${SVG.device}</span><b>${output?('No output'):index===1?'Add second source':'Add a source'}</b><span>${output?'Pair or assign a Bluetooth headset or speaker.':'Pair a phone or computer, then assign it here.'}</span>${btn('Add device','go-devices')}`,'empty-slot node-card');
  const connected=d.connected, state=connected?'connected':d.status==='connecting'?'connecting':d.status==='error'?'error':'disconnected';
  const btVolume=Math.max(0,Math.min(100,model.pendingVolumes[d.addr]?.value ?? (d.volumeKnown?finite(d.volume,100):100)));
  if(d.volumeKnown&&btVolume>0) model.btVolumeMemory[d.addr]=btVolume;
  const btMuted=d.volumeKnown&&btVolume===0;
  const btControl=d.connected&&d.volumeKnown;
  return card(`<div class="device-title"><div><small class="micro-label">${label}</small><b title="${attr(d.name)}">${esc(d.name)}</b></div>${pill(state,connected?'Connected':d.status==='connecting'?'Connecting…':d.status==='error'?'Connection failed':'Disconnected')}</div>
    <div class="node-meta">${d.rssi?`<span>${signal(d.rssi)} ${d.rssi} dBm</span>`:''}<span>${esc(d.codec||'—')} · ${fmtRate(d.rate)}${d.latencyMs?` · ${d.latencyMs} ms`:''}</span></div>
    <div class="node-level"><button class="speaker-toggle ${btMuted?'muted':''}" data-bt-mute="${attr(d.addr)}" aria-pressed="${btMuted?'true':'false'}" title="${btMuted?'Unmute':'Mute'} Bluetooth volume" aria-label="${btMuted?'Unmute':'Mute'} Bluetooth volume" ${btControl?'':'disabled'}>${btMuted?SVG.speakerOff:SVG.speaker}</button><input data-bt-volume="${attr(d.addr)}" type="range" min="0" max="100" step="1" value="${btVolume}" ${btControl?'':'disabled'}><output data-bt-volume-output="${attr(d.addr)}">${d.volumeKnown?`${btVolume}%`:'—'}</output></div>
    ${d.reason?`<p class="node-reason">${esc(d.reason)}</p>`:''}
    <div class="node-actions">${assignedAction(d)}${autoToggle(d)}</div>
    ${!output?`<div class="node-settings">${iconBtn(SVG.settings,'Audio and receiver settings','go-audio')}</div>`:''}`,'node-card',`${output?'output':'input'}-${index}`);
}
// One curve per input slot, all converging on the mixer's centre. Paths are
// rewritten after render by alignConnectors from the measured card centres, so
// this only needs to provide a sane first paint and the right number of paths.
function mergeConnector(list){
  const arr=asArray(list);
  const n=Math.max(1,arr.length);
  const paths=arr.map((d,i)=>{
    const y=Math.round(((i+0.5)/n)*400);
    return `<path class="${connClass(d)}" d="M0 ${y} C 24 ${y}, 24 200, 48 200"/>`;
  }).join('');
  return `<div class="connector-svg merge" data-conn="merge"><svg viewBox="0 0 48 400" preserveAspectRatio="none" aria-hidden="true">${paths}<circle cx="45" cy="200" r="2.5"/></svg></div>`;
}
// The out connector represents the mixer feeding the output group, so it takes
// the most-connected state of the assigned outputs.
function outConnector(list){
  const arr=asArray(list).filter(Boolean);
  const d=arr.find(x=>x.connected)||arr.find(x=>x.status==='connecting')||arr[0];
  return `<div class="connector-svg out" data-conn="out"><svg viewBox="0 0 48 400" preserveAspectRatio="none" aria-hidden="true"><path class="${connClass(d)}" d="M0 200 C 24 200, 24 82, 48 82"/><circle cx="3" cy="200" r="2.5"/></svg></div>`}

// The connector curves must attach to the real card centres. Card heights depend
// on content (empty slots, the receiver disclosure, an expanded details panel), so
// fixed viewBox proportions cannot hold. Measure after each render and rewrite the
// paths; the markup above keeps a sensible shape for the first paint.
function alignConnectors(){
  const rail=app.querySelector('.signal-rail');
  if(!rail)return;
  const rr=rail.getBoundingClientRect();
  if(rr.height<1)return;
  const vy=el=>{const r=el.getBoundingClientRect();return ((r.top+r.height/2)-rr.top)/rr.height*400;};
  const mixer=rail.querySelector('.card.mixer');
  if(!mixer)return;
  const my=+vy(mixer).toFixed(1);
  const merge=rail.querySelector('[data-conn="merge"]');
  const ins=[...rail.querySelectorAll('.input-column .node-card')];
  if(merge&&ins.length){
    const paths=merge.querySelectorAll('path');
    ins.forEach((card,i)=>{
      const cy=+vy(card).toFixed(1);
      if(paths[i])paths[i].setAttribute('d',`M0 ${cy} C 24 ${cy}, 24 ${my}, 48 ${my}`);
    });
    merge.querySelector('circle')?.setAttribute('cy',my);
  }
  const out=rail.querySelector('[data-conn="out"]');
  // With two outputs, attach to the midpoint of the output cards so the curve
  // reads as feeding the group. With one output this is exactly that card's
  // centre, so single-output geometry is unchanged.
  const outCards=[...rail.querySelectorAll('.output-column .node-card')];
  if(out&&outCards.length){
    const centres=outCards.map(vy);
    const cy=+((Math.min(...centres)+Math.max(...centres))/2).toFixed(1);
    out.querySelector('path')?.setAttribute('d',`M0 ${my} C 24 ${my}, 24 ${cy}, 48 ${cy}`);
    out.querySelector('circle')?.setAttribute('cy',my);
  }
}
let alignQueued=false;
function queueAlignConnectors(){
  if(alignQueued)return;
  alignQueued=true;
  requestAnimationFrame(()=>{alignQueued=false;alignConnectors();});
}

function dashboardPage(){
  const s=model.state,inDevs=s.slots.inputs.map(getDevice),outs=s.slots.outputs.map(getDevice);
  // Only assigned inputs are rendered: with up to four slots, empty cards are
  // noise. Slot indices are preserved so node labels and mixer control indices
  // keep matching the configuration (data-mix-gain must stay the slot number).
  const shownIn=inDevs.map((d,i)=>({d,i})).filter(x=>x.d);
  if(!model.localMixer)model.localMixer=structuredClone(s.mixer);
  const m=model.localMixer;
  return `<div class="page dashboard">${healthStrip()}${(()=>{const used=model.state.slots.inputs.filter(Boolean).length;const cap=model.state.inputCapacity;return used>cap.proven?`<div class="warning"><span class="warn-icon">${SVG.warning}</span><span class="warn-text"><b>${used} inputs in use — experimental.</b> Only ${cap.proven} are validated; dropouts are likely.</span></div>`:'';})()}${model.mixerError?`<div class="warning">${esc(model.mixerError)}${btn('Retry mixer save','mixer-retry')}</div>`:''}${s.health.wifi.warning?`<div class="warning"><span class="warn-icon">${SVG.warning}</span><span class="warn-text">2.4 GHz Wi‑Fi competes with Bluetooth audio. 5 GHz is recommended for multiple streams.</span>${btn('Network','go-network','ghost')}</div>`:''}
    <div class="path-map">${shownIn.map((x,i)=>(i?'<span>+</span>':'')+pill(x.d.connected?'connected':'disconnected',x.d.name||('Input '+(x.i+1)))).join('')}<span>→</span>${outs.filter(Boolean).map(o=>pill(o.connected?'connected':'disconnected',o.name||'Output')).join('')}</div>
    <div class="signal-rail legacy-rail"><div class="input-column">${shownIn.length?shownIn.map(x=>nodeCard('Input '+(x.i+1),x.d,x.i,m)).join(''):nodeCard('Input 1',undefined,0,m)}</div>${mergeConnector(shownIn.length?shownIn.map(x=>x.d):[undefined])}
    ${card(`<div class="mixer-head"><div><small>Mixer</small><b>Headroom ${m.headroomDb} dB</b></div>${iconBtn(SVG.settings,'Mixer and audio settings','go-audio')}</div>
    ${shownIn.map(x=>`<div class="channel"><div class="channel-top"><span>Input ${x.i+1}</span><input data-mix-gain="${x.i}" type="range" min="-30" max="6" step="1" value="${m.gains[x.i]||0}"><output data-mix-output="${x.i}">${m.gains[x.i]||0} dB</output><button class="btn ${m.mutes[x.i]?'primary':'ghost'}" data-mute="${x.i}" title="${m.mutes[x.i]?'Unmute':'Mute'} Input ${x.i+1} in the mixer">${m.mutes[x.i]?'Unmute':'Mute'}</button></div><div class="channel-bottom"><small>Play on</small><div class="segments">${[['stereo','Both'],['left','L'],['right','R']].map(([v,l])=>`<button data-place="${x.i}:${v}" class="${m.placement[x.i]===v?'active':''}" title="${v==='stereo'?'Both ears':v==='left'?'Left ear only':'Right ear only'}">${l}</button>`).join('')}</div><small>${m.placement[x.i]==='stereo'?'Stereo':m.placement[x.i]==='left'?'Mono to left ear':'Mono to right ear'}</small></div></div>`).join('')}
    <div class="master"><span>Master</span><input id="master-gain" type="range" min="-30" max="6" value="${m.master}"><output id="master-output">${m.master} dB</output><button class="btn ${m.masterMute?'primary':'ghost'}" data-action="master-mute" title="${m.masterMute?'Unmute':'Mute'} the mix">${m.masterMute?'Unmute':'Mute'}</button></div>`,'mixer')}
    ${outConnector(outs)}<div class="output-column">${nodeCard('Output',outs[0],0,m,true)}</div></div></div>`;
}

function deviceCard(d){
  const caps=asArray(d.caps), assigned=roleLabel(d.role), busy=model.btBusy;
  const kindLabel=d.kind==='source'?'Sends audio':d.kind==='output'?'Plays audio':d.kind==='audio-bidirectional'?'Sends and plays audio':(d.paired?'Bluetooth device':'Type unknown');
  const supportedRoles=new Set(['',...(caps.includes('sends_audio')?model.state.slots.inputs.map((_,i)=>'in'+(i+1)):[]),...(caps.includes('plays_audio')?['out1']:[])]); const staleRole=d.role&&!supportedRoles.has(d.role)?`<option value="${attr(d.role)}" selected>${esc(roleLabel(d.role))} (device missing)</option>`:''; const roleOptions=`<option value="" ${!d.role?'selected':''}>Unassigned</option>${staleRole}${caps.includes('sends_audio')?model.state.slots.inputs.map((_,i)=>`<option value="in${i+1}" ${d.role==='in'+(i+1)?'selected':''}>Input ${i+1}</option>`).join(''):''}${caps.includes('plays_audio')?`<option value="out1" ${d.role==='out1'?'selected':''}>Output</option>`:''}`;
  const actions=[];
  if(!d.paired) actions.push(btn('Pair',`bt-action:pair:${d.addr}`,'secondary',busy?'disabled':''));
  if(d.paired) actions.push(btn(d.connected?'Disconnect':'Connect',`bt-action:${d.connected?'disconnect':'connect'}:${d.addr}`,'secondary',busy?'disabled':''));
  if(d.paired) actions.push(iconBtn(SVG.trash,'Forget this device',`bt-action:forget:${d.addr}`,'ghost',busy?'disabled':''));
  return card(`<div class="device-head"><span class="device-icon">${iconFor(d)}</span><div class="device-title"><div><small class="micro-label">${esc(kindLabel)}</small><b title="${attr(d.name)}">${esc(d.name)}</b></div>${pill(d.connected?'connected':'disconnected',d.connected?'Connected':d.paired?'Paired':'Available')}</div></div><div class="meta-row"><span>${esc(d.addr||'Unknown address')}</span>${d.rssi?`<span>${signal(d.rssi)}${d.rssi}</span>`:''}</div>
  <label>Role<select data-role-addr="${attr(d.addr)}" ${(!d.paired&&!d.role)||busy?'disabled':''}>${roleOptions}</select><small>${d.role?(d.paired?`Assigned as ${esc(assigned)}`:`${esc(assigned)} assignment is stale — choose Unassigned to clear it`):(d.paired?'Pick a role to route this device':'Not paired — use Pair first, then assign a role')}</small></label>
  <div class="device-actions">${actions.join('')}</div>`,'device-card');
}
function devicesPage(){const s=model.state;let count=0;if(s.pairing.active&&s.pairing.until)count=Math.max(0,Math.ceil((new Date(s.pairing.until)-Date.now())/1000));
  // Paired devices and freshly discovered ones were previously interleaved in one
  // grid, which made it hard to tell an already-set-up device from a new find.
  const assigned=s.devices.filter(d=>d.role);
  const paired=s.devices.filter(d=>!d.role&&d.paired);
  const found=s.devices.filter(d=>!d.role&&!d.paired);
  const grid=arr=>`<div class="device-grid">${arr.map(deviceCard).join('')}</div>`;
  const head=(label,arr)=>`<small>${arr.length}</small>`;
  return `<div class="page"><div class="page-title"><div><h1>Devices</h1><p>Pair Bluetooth devices, assign inputs and choose your outputs.</p></div></div>
  <div class="pairbar"><div><b>${s.pairing.active?`Pairing mode · ${count}s`:'Pairing mode is off'}</b><span>${s.pairing.active?`Open Bluetooth settings on your phone or computer and choose ${esc(s.system.btName)}.`:'Enable pairing only when adding a device.'}</span></div>${iconBtn(SVG.search,s.pairing.scanning?'Scanning…':'Scan for devices','bt-scan','secondary',s.pairing.scanning||model.btBusy?'disabled':'')}${btn(s.pairing.active?'Stop pairing':'Start pairing','bt-pairing',s.pairing.active?'secondary':'primary',model.btBusy?'disabled':'')}</div>
  ${s.devices.length?`${assigned.length?section('Assigned',grid(assigned),head('Assigned',assigned)):''}${paired.length?section('Paired, not assigned',grid(paired),head('Paired',paired)):''}${found.length?section('Discovered, not paired',grid(found),head('Discovered',found)):''}`:card(`<span class="empty-icon">${SVG.device}</span><b>Nothing paired yet</b><p>Start pairing, or scan for a headset.</p>`,'empty-slot')}</div>`}

function networkPage(){const s=model.state,w=s.wifi;return `<div class="page"><div class="page-title"><div><h1>Network</h1><p>Prefer 5 GHz — Bluetooth audio uses 2.4 GHz.</p></div>${iconBtn(SVG.search,'Scan networks','wifi-scan','secondary')}</div>${w.apply?`<div class="apply-banner ${attr(w.apply.state)}"><div><b>${w.apply.state==='verifying'?'Confirm this network':esc(w.apply.state)}</b><span>${esc(w.apply.message||'')}</span></div>${w.apply.state==='verifying'?btn('Confirm','wifi-confirm','primary'):''}</div>`:''}
  <div class="network-grid">${card(`<div class="device-title"><div><small>Connected network</small><b>${esc(w.ssid||'Not connected')}</b></div>${pill(w.ssid?(w.band==='2.4 GHz'?'warning':'connected'):'error',w.band)}</div><div class="stats"><div><small>Signal</small><b>${signal(w.rssi)} ${w.rssi||'—'} dBm</b></div><div><small>Channel</small><b>${w.channel||'—'} · ${w.freq||'—'} MHz</b></div><div><small>IP address</small><b>${esc(w.ip||'—')}</b></div><div><small>Reachable at</small><b>${esc(s.system.mdns)}</b></div></div>${w.band==='2.4 GHz'?'<div class="warning compact">2.4 GHz can cause Bluetooth dropouts under multi-stream load.</div>':''}`)}
  ${card(`<div class="section-head"><span>Other networks</span><small>${w.networks.length} found</small></div><div class="network-list">${w.networks.map(n=>`<div class="network-row"><button class="network-main" data-wifi-select="${attr(n.bssid)}">${signal(n.rssi)}<span>${esc(n.ssid)}</span><em>${esc(n.band)}</em><small>${n.rssi} dBm</small></button>${model.joinBssid===n.bssid?`<div class="join-form">${n.secure?'<input id="join-password" type="password" placeholder="Wi‑Fi password">':''}<p>The hub arms a 60-second rollback before switching networks.</p><button class="btn primary" data-wifi-join="${attr(n.bssid)}">Join ${esc(n.ssid)}</button></div>`:''}</div>`).join('')}</div>`)}</div></div>`}

// Only combinations the backend accepts: period a multiple of 10 ms, buffer an
// exact multiple of the period and at least 3 periods. Free numeric fields let
// users enter values the daemon then rejects; a selection cannot.
const QUANTA=[1024,2048,4096,8192];
const BUFFER_PAIRS=[
  {p:50000,b:200000,label:'Low latency'},
  {p:100000,b:300000,label:'Balanced'},
  {p:100000,b:500000,label:'Safe'},
  {p:200000,b:600000,label:'Maximum stability'},
];
const presets={low:{preset:'low',preferredRate:48000,allowedRates:[44100,48000],quantum:1024,bluealsaPeriodUs:50000,bluealsaBufferUs:200000,resampler:'auto',codecPolicy:'compatibility'},balanced:{preset:'balanced',preferredRate:48000,allowedRates:[44100,48000],quantum:2048,bluealsaPeriodUs:100000,bluealsaBufferUs:500000,resampler:'auto',codecPolicy:'compatibility'},stable:{preset:'stable',preferredRate:48000,allowedRates:[44100,48000],quantum:4096,bluealsaPeriodUs:100000,bluealsaBufferUs:500000,resampler:'auto',codecPolicy:'compatibility'}};
function audioPage(){const s=model.state;if(!model.localAudio)model.localAudio=structuredClone(s.audio);const a=model.localAudio;const dirty=JSON.stringify(a)!==JSON.stringify(s.audio);return `<div class="page audio-page"><div class="page-title"><div><h1>Audio</h1><p>Latency and buffering. Applying restarts the audio graph.</p></div></div>
  ${section('Presets',`<div class="preset-row">${[['low','Low latency'],['balanced','Balanced'],['stable','Maximum stability']].map(([k,l])=>`<button data-preset="${k}" class="${a.preset===k?'active':''}" title="Set quantum and buffers for ${l.toLowerCase()}"><b>${l}</b><small>${k==='low'?'q1024 · 50/200 ms':k==='balanced'?'q2048 · 100/500 ms':'q4096 · 100/500 ms'}</small></button>`).join('')}</div>`)}
  <div class="settings-grid">${card(`<h2>Sampling</h2><label><span class="label-row">Preferred graph rate${hint('The clock PipeWire runs the graph at. 48 kHz suits most Bluetooth codecs; 44.1 kHz avoids a resample for CD-rate sources.')}</span><select data-audio="preferredRate"><option value="44100" ${a.preferredRate===44100?'selected':''}>44.1 kHz</option><option value="48000" ${a.preferredRate===48000?'selected':''}>48 kHz</option><option value="96000" ${a.preferredRate===96000?'selected':''}>96 kHz</option></select></label><label class="check"><input data-rate="44100" type="checkbox" ${a.allowedRates.includes(44100)?'checked':''}>Allow 44.1 kHz</label><label class="check"><input data-rate="48000" type="checkbox" ${a.allowedRates.includes(48000)?'checked':''}>Allow 48 kHz</label>`)}
  ${card(`<h2>Buffers & latency</h2><label><span class="label-row">PipeWire quantum${hint('Samples processed per graph cycle. Lower is lower latency and more CPU; higher is more stable under load.')}</span><select data-audio="quantum">${QUANTA.map(q=>`<option value="${q}" ${a.quantum===q?'selected':''}>${q} samples</option>`).join('')}${QUANTA.includes(a.quantum)?'':`<option value="${a.quantum}" selected>${a.quantum} samples (custom)</option>`}</select></label><label><span class="label-row">BlueALSA buffering${hint('How often the secondary receiver hands audio to PipeWire, and how much jitter it absorbs. Paired because the buffer must be an exact multiple of the period. Higher values survive more load and add latency.')}</span><select data-buffer-pair>${BUFFER_PAIRS.map(q=>`<option value="${q.p}:${q.b}" ${(a.bluealsaPeriodUs===q.p&&a.bluealsaBufferUs===q.b)?'selected':''}>${q.label} · ${q.p/1000}/${q.b/1000} ms</option>`).join('')}${BUFFER_PAIRS.some(q=>q.p===a.bluealsaPeriodUs&&q.b===a.bluealsaBufferUs)?'':`<option value="${a.bluealsaPeriodUs}:${a.bluealsaBufferUs}" selected>Custom · ${a.bluealsaPeriodUs/1000}/${a.bluealsaBufferUs/1000} ms</option>`}</select></label>`)}
  ${card(`<h2>Codec policy</h2><label><span class="label-row">Bluetooth codec${hint('Compatibility restricts the hub to SBC, which every headset supports. Quality allows other codecs when both ends agree.')}</span><select data-audio="codecPolicy"><option value="compatibility" ${a.codecPolicy==='compatibility'?'selected':''}>Compatibility (SBC)</option><option value="quality" ${a.codecPolicy==='quality'?'selected':''}>Quality when available</option></select></label>`)}
  ${card(`<h2>BlueALSA receiver</h2><p class="settings-note">One daemon serves ${esc(model.state.receiverInputs||'the inputs after the first')}. Its SBC maximum is a per-process setting, so it is <b>one value for all of them</b> — not one per input.</p><label><span class="label-row">SBC maximum bitpool${hint('Upper bound on SBC data rate, negotiated per connection. Lower values need less radio airtime and can fix dropouts with several sources; they also reduce quality. One value for every BlueALSA input.')}</span><select data-audio="secondarySbcMaxBitpool">${[35,53,64,250].map(n=>`<option value="${n}" ${a.secondarySbcMaxBitpool===n?'selected':''}>${n}${n===35?' · Compatibility':''}</option>`).join('')}</select></label><p>Input 2 and beyond. A source must reconnect to renegotiate.</p>`)}
  ${delayCard(a)}</div>${dirty?`<div class="applybar"><span>Audio settings have unapplied changes.</span>${iconBtn(SVG.undo,'Revert changes','audio-revert')}${btn('Apply & restart audio','audio-apply','primary')}</div>`:''}</div>`}

const DELAY_LABEL={default:['disconnected','Engine default'],unsupported:['warning','Not supported here'],pending:['connecting','Pending confirmation'],reported:['connected','Reported'],rejected:['error','Rejected'],mismatch:['warning','Not confirmed']};

// The rendering-delay report is only meaningful on the secondary (BlueALSA) input
// and only the process that acquired the transport may write it, so this card
// shows what was actually observed rather than what was saved. "Reported" means
// the acquired transport currently exposes the requested total; it is not proof
// that a source application corrected its video.
function delayCard(a){
  const r=model.state.delayReport||normalizeDelay({});
  const [dotState,label]=DELAY_LABEL[r.state]||DELAY_LABEL.pending;
  const out=getDevice(model.state.slots.outputs[0]);
  const rows=[
    ['Requested',`${r.requestedMs} ms`],
    ['Transport reports',r.actualKnown?`${r.actualMs} ms`:'not exposed by BlueZ'],
  ];
  if(r.attempt)rows.push(['Last attempt',r.attempt+(r.attemptedAt?` · ${new Date(r.attemptedAt).toLocaleTimeString()}`:'')]);
  rows.push(['Applies to',r.input||'Input 2 (BlueALSA receiver)']);
  if(r.addr)rows.push(['Transport',r.addr]);
  if(out&&out.latencyMs)rows.push(['Output path',`Output 1 reports ${out.latencyMs} ms — already part of the total, do not add it again`]);
  return card(`<h2>A/V synchronization · experimental</h2>
    <div class="delay-scope"><span class="pill">${dot(dotState)}${esc(label)}</span><span>Input 2 and beyond (BlueALSA receiver). Input 1 is PipeWire and unaffected.</span></div>
    <label><span class="label-row">Advertised total sink delay (ms)${hint('Tells the source how long audio takes to reach your ears, so it can line video up. Enter a measured total, not an offset. 0 reports nothing and keeps the engine default.')}</span><input data-audio="secondaryAdvertisedDelayMs" type="number" min="0" max="2000" step="10" value="${finite(a.secondaryAdvertisedDelayMs,0)}"></label>
    <div class="delay-actions">${btn('Apply to receiver','delay-apply','primary')}${btn('Reset to 0','delay-reset')}</div>
    <dl class="delay-status">${rows.map(([k,v])=>`<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('')}</dl>
    ${r.detail?`<p class="settings-note">${esc(r.detail)}</p>`:''}
    <details class="calibration"><summary>How to calibrate this value</summary><ol><li>Play the same clip on the source and measure the audio-to-video offset at your ears — that measurement is the total.</li><li>Enter that total in milliseconds and press <b>Apply to receiver</b>.</li><li>The source must reconnect before it can adopt a new report. Watch the status change from Pending confirmation.</li><li>Measure again. BlueZ accepting the report does not prove the source corrected its video; only re-measuring does.</li></ol><p>No separate offset: a residual error goes into this total, not on top of it, and latency you measured from the headphones is already included.</p></details>`,'delay-card');
}

async function applyDelay(ms){
  const value=Math.max(0,Math.min(2000,Math.round(Number(ms)||0)));
  try{
    const r=await post('/api/audio/delay',{ms:value});
    if(model.localAudio)model.localAudio.secondaryAdvertisedDelayMs=value;
    if(r&&r.delayReport&&model.state)model.state.delayReport=normalizeDelay(r.delayReport);
    // The field is guarded while the user edits it, so an explicit apply has to
    // release it or the control would keep showing the old typed value.
    releaseControl(document.querySelector('[data-audio="secondaryAdvertisedDelayMs"]'));
    toast(value===0?'Delay report reset to the engine default.':'Requested a '+value+' ms total. Waiting for the receiver to confirm.');
  }catch(e){toast(e.message,'error')}
  render();
}

function systemPage(){const s=model.state.system;return `<div class="page"><div class="page-title"><div><h1>System</h1><p>Identity and maintenance.</p></div></div><div class="settings-grid">${card(`<h2>Identity</h2><label><span class="label-row">Hostname${hint('Names the hub on the network. Reachable at <hostname>.local and used for the web address.')}</span><input id="system-hostname" value="${attr(s.hostname)}"></label><label><span class="label-row">Bluetooth name${hint('What phones and computers see when they scan for this hub.')}</span><input id="system-btname" value="${attr(s.btName)}"></label>${btn('Save identity','identity-save','primary')}`)}
  ${(()=>{const u=model.update||{};const busy=!!u.running;return card(`<h2>About</h2><dl><dt>OpenAudioHub</dt><dd>${esc(s.version)}</dd><dt>Operating system</dt><dd>${esc(s.os)}</dd><dt>Uptime</dt><dd>${esc(s.uptime)}</dd><dt>Current time</dt><dd>${esc(new Date(s.time).toLocaleString())}</dd></dl>
    <div class="update-row">${u.checked?(u.available?`<span class="pill">${dot('warning')}${esc(u.latest)} available</span>`:`<span class="pill">${dot('connected')}Up to date</span>`):''}</div>
    ${u.detail?`<p class="settings-note">${esc(u.detail)}</p>`:''}
    ${u.checksError?`<p class="settings-note">${esc(u.checksError)}</p>`:''}
    <div class="stack-actions">${btn(busy?'Updating…':'Check for updates','update-check','secondary',busy?'disabled':'')}${u.available?btn('Update to '+u.latest,'update-apply','primary',busy?'disabled':''):''}</div>
    ${u.notes?`<details class="calibration"><summary>What changed in ${esc(u.latest)}</summary><pre class="log">${esc(u.notes)}</pre></details>`:''}`)})()}
  ${card(`<h2>Maintenance</h2><div class="stack-actions">${iconBtn(SVG.refresh,'Restart the audio graph','system:restart-audio')}<a class="btn secondary" href="/api/system/backup">Download configuration backup</a>${btn('Restore from backup','restore-backup','secondary')}<input id="restore-file" type="file" accept=".zip,application/zip" hidden><p class="settings-note">A backup contains config.json, audio.json and wireplumber.conf. Restoring applies the first two — configuration and audio settings — and reconnects audio. If the backup has a different password you will be signed out.</p>${btn('Reboot hub','system:reboot','danger')}</div>`)}
  ${card(`<h2>Change password</h2><label>Current password<input id="pw-current" type="password"></label><label>New password<input id="pw-new" type="password"></label><label>Repeat new password<input id="pw-again" type="password"></label>${btn('Change password','password-change')}`)}</div></div>`}

function diagnosticsPage(){const d=model.diagnostics||model.state.diagnostics;if(!d)return `<div class="page"><div class="page-title"><h1>Diagnostics</h1></div>${iconBtn(SVG.refresh,'Load diagnostics','diag-refresh','primary')}</div>`;return `<div class="page"><div class="page-title"><div><h1>Diagnostics</h1><p>Health, transports and logs.</p></div><div>${iconBtn(SVG.refresh,'Refresh diagnostics','diag-refresh')} ${iconBtn(SVG.copy,'Copy diagnostics','diag-copy')}</div></div><div class="diag-cards">${card('<small>CPU</small><b>'+d.cpuPercent.toFixed(1)+'%</b>')}${card('<small>Memory</small><b>'+d.memPercent.toFixed(1)+'%</b>')}${card(`<small>Temperature</small><b>${d.tempC?d.tempC.toFixed(1)+' °C':'—'}</b>`)}${card('<small>PipeWire errors</small><b>'+d.xruns+'</b>')}</div>
  ${section('Assigned audio paths',`<div class="table">${model.state.devices.filter(x=>x.role).map(x=>`<div class="tr"><span>${esc(roleLabel(x.role))}</span><span>${esc(x.name)}</span><span>${esc(x.backend||'not active')}</span><span>${x.connected?'connected':'disconnected'}</span></div>`).join('')||'<div class="table-empty">No roles assigned.</div>'}</div>`)}
  ${section('Bluetooth transports',`<div class="table">${d.transports.map(t=>`<div class="tr"><span>${esc(t.addr)}</span><span>${esc(t.codec||'—')}</span><span>${fmtRate(t.rate)}</span><span>${esc(t.state)}</span></div>`).join('')||'<div class="table-empty">No active Bluetooth transports.</div>'}</div>`)}
  ${section('Services',`<div class="service-list">${d.services.map(x=>`<div>${pill(x.state==='active'?'connected':'error',x.state||'unknown')}<span>${esc(x.name)}</span></div>`).join('')}</div>`)}
  ${section('Recent OpenAudioHub log',`<pre class="log">${esc(d.logs.join('\n'))}</pre>`)}</div>`}

async function loadPublic(){
  try{
    model.publicInfo=await api('/api/public');
    model.locked=!model.publicInfo.authenticated;
    if(!model.locked){
      try{setState(await api('/api/state'));openEvents();}
      catch{model.state=null;model.stateError='Hub state is temporarily unavailable. Retry in a moment.';}
    }
    render();
  }catch{
    app.innerHTML=`<div class="loading">${logo()}<span>OpenAudioHub is temporarily unavailable.</span>${btn('Retry','ui-retry','primary')}</div>`;
  }
}
function openEvents(){
  if(model.eventSource)return;
  const es=new EventSource('/events'); model.eventSource=es; model.realtime='connecting';
  es.onopen=()=>{model.realtime='connected'; if(model.state)render(true);};
  es.addEventListener('state',e=>{try{setState(JSON.parse(e.data));render(true)}catch(err){console.error('Ignored invalid state event',err)}});
  es.onerror=()=>{model.realtime='reconnecting'; if(model.state)render(true);};
}
function closeEvents(){model.eventSource?.close();model.eventSource=null;model.realtime='connecting'}
async function commitMixer(){
  const edit=++model.mixerEdit,snapshot=structuredClone(model.localMixer);
  model.mixerDirty=true;model.mixerError='';
  try{
    await post('/api/mixer',snapshot);
    if(edit===model.mixerEdit){model.mixerDirty=false;model.mixerError='';model.state.mixer=structuredClone(snapshot);for(const el of app.querySelectorAll('[data-mix-gain],#master-gain'))releaseControl(el);}
  }catch(e){if(edit===model.mixerEdit)model.mixerError=e.message;toast(e.message,'error');}
  render(true);
}
async function commitBTVolume(addr,value){
  const pending={value,acknowledged:false,until:Infinity};model.pendingVolumes[addr]=pending;
  try { await post('/api/bluetooth/volume',{addr,volume:value});pending.acknowledged=true;pending.until=Date.now()+30000; }
  catch(e){if(model.pendingVolumes[addr]===pending)delete model.pendingVolumes[addr];toast(e.message,'error');}
  if(model.pendingVolumes[addr]===pending || !model.pendingVolumes[addr])releaseControl(document.querySelector(`[data-bt-volume="${CSS.escape(addr)}"]`));render(true);
}

app.addEventListener('change',async e=>{
  if(e.target.id!=='restore-file')return;
  const file=e.target.files&&e.target.files[0];
  e.target.value='';
  if(!file)return;
  if(!confirm(`Restore "${file.name}"? This replaces the hub configuration and audio settings, and reconnects audio. If the backup has a different password you will be signed out.`))return;
  try{
    const r=await restoreBackup(file);
    toast('Backup restored. Reloading state.');
    try{setState(await api('/api/state'));}catch{}
    render();
  }catch(x){toast(x.message,'error');}
});

app.addEventListener('submit',async e=>{
  if(e.target.id!=='login-form')return;
  e.preventDefault();
  const err=document.querySelector('#login-error'), submit=e.target.querySelector('button[type="submit"]');
  if(submit) submit.disabled=true;
  if(err){err.hidden=true;err.querySelector('span').textContent='';}
  try{
    await post('/api/session',{password:document.querySelector('#login-password').value,remember:document.querySelector('#login-remember').checked});
  }catch(x){
    if(err){err.hidden=false;err.querySelector('span').textContent=x.message==='Incorrect password'?'Incorrect password':'Unable to sign in right now.';}
    if(submit) submit.disabled=false;
    return;
  }
  model.publicInfo=await api('/api/public').catch(()=>model.publicInfo);
  model.locked=false; model.route='Dashboard';
  try{setState(await api('/api/state'));openEvents();}
  catch{model.state=null;model.stateError='Signed in, but hub state is temporarily unavailable.';}
  render();
});

app.addEventListener('input',e=>{
  if(e.target.dataset.mixGain!==undefined){const i=+e.target.dataset.mixGain;model.mixerDirty=true;model.mixerEdit++;model.localMixer.gains[i]=+e.target.value;document.querySelector(`[data-mix-output="${i}"]`).textContent=`${e.target.value} dB`;}
  if(e.target.id==='master-gain'){model.mixerDirty=true;model.mixerEdit++;model.localMixer.master=+e.target.value;document.querySelector('#master-output').textContent=`${e.target.value} dB`;}
  if(e.target.dataset.btVolume!==undefined){const addr=e.target.dataset.btVolume;const v=+e.target.value;model.pendingVolumes[addr]={value:v,acknowledged:false,until:Infinity};if(v>0)model.btVolumeMemory[addr]=v;const o=document.querySelector(`[data-bt-volume-output="${CSS.escape(addr)}"]`);if(o)o.textContent=`${v}%`;}
  if(e.target.dataset.bufferPair!==undefined){const [bp,bb]=e.target.value.split(':').map(Number);model.localAudio.bluealsaPeriodUs=bp;model.localAudio.bluealsaBufferUs=bb;model.localAudio.preset='custom';render();}
  if(e.target.dataset.audio){const k=e.target.dataset.audio;model.localAudio[k]=e.target.type==='checkbox'?e.target.checked:(e.target.type==='number'||k==='preferredRate'||k==='secondarySbcMaxBitpool'?+e.target.value:e.target.value);model.localAudio.preset='custom';render();}
  if(e.target.dataset.rate){const n=+e.target.dataset.rate;model.localAudio.allowedRates=e.target.checked?[...new Set([...model.localAudio.allowedRates,n])]:model.localAudio.allowedRates.filter(x=>x!==n);model.localAudio.preset='custom';render();}
});
app.addEventListener('change',async e=>{
  if(e.target.dataset.mixGain!==undefined||e.target.id==='master-gain')await commitMixer();
  if(e.target.dataset.btVolume!==undefined)await commitBTVolume(e.target.dataset.btVolume,+e.target.value);
  if(e.target.dataset.roleAddr){if(model.btBusy)return;model.btBusy=true;try{await post('/api/bluetooth/role',{addr:e.target.dataset.roleAddr,role:e.target.value});setState(await api('/api/state'));toast('Role updated')}catch(x){toast(x.message,'error')}finally{model.btBusy=false;render()}}
});

app.addEventListener('click',async e=>{
  const r=e.target.closest('[data-route]')?.dataset.route;if(r){model.route=r;if(r==='Diagnostics'&&!model.diagnostics)loadDiagnostics();render();return}
  const p=e.target.closest('[data-place]')?.dataset.place;if(p){const[i,v]=p.split(':');model.localMixer.placement[+i]=v;render();await commitMixer();return}
  const mute=e.target.closest('[data-mute]')?.dataset.mute;if(mute!==undefined){const i=+mute;model.localMixer.mutes[i]=!model.localMixer.mutes[i];render();await commitMixer();return}
  const preset=e.target.closest('[data-preset]')?.dataset.preset;if(preset){model.localAudio={...model.localAudio,...presets[preset],liveMeters:model.localAudio.liveMeters};releaseControls(app);render();return}
  const bssid=e.target.closest('[data-wifi-select]')?.dataset.wifiSelect;if(bssid){model.joinBssid=model.joinBssid===bssid?'':bssid;render();return}
  const join=e.target.closest('[data-wifi-join]')?.dataset.wifiJoin;if(join){const n=model.state.wifi.networks.find(x=>x.bssid===join);if(!n){model.joinBssid='';render();toast('The network list changed. Scan again.','error');return}try{await post('/api/network/join',{ssid:n.ssid,password:document.querySelector('#join-password')?.value||'',bssid:n.bssid,band:n.band==='5 GHz'?'5GHz':'2.4GHz'});toast('Switching Wi‑Fi. Reconnect if the page drops.')}catch(x){toast(x.message,'error')}return}
  const btAuto=e.target.closest('[data-bt-auto]');if(btAuto){if(model.btBusy)return;model.btBusy=true;try{await post('/api/bluetooth/auto',{addr:btAuto.dataset.btAuto,enabled:btAuto.dataset.autoEnabled!=='1'});setState(await api('/api/state'));toast('Auto-connect updated')}catch(x){toast(x.message,'error')}finally{model.btBusy=false;render()}return}
  const btMute=e.target.closest('[data-bt-mute]');if(btMute){const addr=btMute.dataset.btMute,d=getDevice(addr);if(!d?.connected)return;const current=Math.max(0,Math.min(100,model.pendingVolumes[addr]?.value??finite(d.volume,100)));if(current>0)model.btVolumeMemory[addr]=current;const target=current===0?(model.btVolumeMemory[addr]||70):0;try{await commitBTVolume(addr,target);render();toast(target===0?'Bluetooth audio muted':'Bluetooth audio unmuted')}catch(x){toast(x.message,'error')}return}
  const action=e.target.closest('[data-action]')?.dataset.action;if(!action)return;
  try{
    if(action==='noop')return;
    if(action==='theme-toggle'){cycleTheme();render();return;}
    if(action==='restore-backup'){document.querySelector('#restore-file').click();return;}
    if(action==='update-check'){try{model.update=await api('/api/system/update');render();}catch(x){toast(x.message,'error');}return;}
    if(action==='update-apply'){const u=model.update||{};if(!confirm(`Update to ${u.latest}? The hub reinstalls itself and audio is interrupted; this takes a few minutes.`))return;try{await post('/api/system/update',{});model.update={...(model.update||{}),running:true};toast('Update started. Watch Diagnostics for progress.');render();}catch(x){toast(x.message,'error');}return;}
    if(action==='mixer-retry'){await commitMixer();return;}
    if(action==='logout'){model.localMixer=null;model.localAudio=null;model.minRevision=0;await api('/api/session',{method:'DELETE'});model.locked=true;model.state=null;model.route='Dashboard';closeEvents();render();return}
    if(action==='ui-retry'){model.stateError='';await loadPublic();return}if(action==='ui-dashboard'){model.route='Dashboard';render();return}if(action==='go-devices'){model.route='Devices';render();return}if(action==='go-network'){model.route='Network';render();return}if(action==='go-audio'){model.route='Audio';render();return}
    if(action==='master-mute'){model.localMixer.masterMute=!model.localMixer.masterMute;render();await commitMixer();return}
    if(action==='bt-scan'){await post('/api/bluetooth/scan',{});toast('Bluetooth scan started');return}
    if(action==='bt-pairing'){await post('/api/bluetooth/pairing',{enabled:!model.state.pairing.active});return}
    if(action.startsWith('bt-action:')){const[,act,...parts]=action.split(':');const addr=parts.join(':');const d=getDevice(addr);if(model.btBusy)return;if(act==='forget'){const role=d?.role?` It will also clear ${roleLabel(d.role)}.`:'';if(!confirm(`Forget ${d?.name||addr}?${role}`))return;}model.btBusy=true;render();try{await post('/api/bluetooth/action',{addr,action:act});setState(await api('/api/state'));toast(act==='forget'?'Device forgotten':`${act} requested`)}finally{model.btBusy=false;render()}return}
    if(action==='wifi-scan'){await post('/api/network/scan',{});toast('Wi‑Fi scan complete');return}
    if(action==='wifi-confirm'){await post('/api/network/confirm',{id:model.state.wifi.apply.id});toast('Network confirmed');return}
    if(action==='delay-apply'){await applyDelay(document.querySelector('[data-audio="secondaryAdvertisedDelayMs"]').value);return;}
    if(action==='delay-reset'){await applyDelay(0);return;}
    if(action==='audio-revert'){model.localAudio=structuredClone(model.state.audio);releaseControls(app);render();return}
    if(action==='audio-apply'){
      if(!confirm('Apply audio settings? Receiver/graph changes may briefly interrupt playback.'))return;
      const snapshot=structuredClone(model.localAudio);await post('/api/audio',snapshot);
      model.state.audio=snapshot;releaseControls(app);render();toast('Audio settings applied');return;
    }
    if(action==='identity-save'){await post('/api/system/identity',{hostname:document.querySelector('#system-hostname').value,btName:document.querySelector('#system-btname').value});releaseControl(document.querySelector('#system-hostname'));releaseControl(document.querySelector('#system-btname'));toast('Identity update requested');return}
    if(action.startsWith('system:')){const act=action.split(':')[1];if(act==='reboot'&&!confirm('Reboot OpenAudioHub now?'))return;await post('/api/system/action',{action:act});toast(`${act} requested`);return}
    if(action==='password-change'){const cur=document.querySelector('#pw-current').value,n=document.querySelector('#pw-new').value,a=document.querySelector('#pw-again').value;if(n!==a)throw new Error('New passwords do not match');await post('/api/system/password',{Current:cur,New:n});toast('Password changed');model.locked=true;model.state=null;closeEvents();render();return}
    if(action==='diag-refresh'){await loadDiagnostics();render();return}
    if(action==='diag-copy'){await navigator.clipboard.writeText(JSON.stringify({system:model.state.system,wifi:model.state.wifi,health:model.state.health,devices:model.state.devices.filter(x=>x.role||x.connected),diagnostics:model.diagnostics},null,2));toast('Diagnostics copied');return}
  }catch(x){toast(x.message,'error')}
});

// A backup is uploaded as raw bytes; api() would JSON-encode it and corrupt the
// zip, so this posts the file directly.
async function restoreBackup(file){
  const res=await fetch('/api/system/restore',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/zip'},body:file});
  if(res.status===401){model.locked=true;model.state=null;closeEvents();render();throw new Error('Signed out. Sign in and restore again.');}
  if(!res.ok){let m='The backup could not be restored.';try{const j=await res.json();if(j?.error)m=j.error;}catch{}throw new Error(m);}
  return res.json().catch(()=>null);
}

async function loadDiagnostics(){try{const s=normalizeState(await api('/api/diagnostics'));model.diagnostics=normalizeDiagnostics(s.diagnostics)}catch(e){toast(e.message,'error')}}
loadPublic();
