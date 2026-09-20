import {patchHTML, installInteractionGuards, interacting, releaseControl, releaseControls} from './dom-sync.js';
const app = document.querySelector('#app');

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
const roleLabel = r => ({in1:'Input 1',in2:'Input 2',out1:'Output 1',out2:'Output 2'}[r] || 'Unassigned');
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
  const inputs = asArray(slots.inputs).slice(0,2).map(v=>String(v||'')); while(inputs.length<2) inputs.push('');
  const outputs = asArray(slots.outputs).slice(0,2).map(v=>String(v||'')); while(outputs.length<2) outputs.push('');
  const gains = asArray(mixer.gains).slice(0,2).map(v=>finite(v)); while(gains.length<2) gains.push(0);
  const mutes = asArray(mixer.mutes).slice(0,2).map(Boolean); while(mutes.length<2) mutes.push(false);
  const placement = asArray(mixer.placement).slice(0,2).map(v=>['stereo','left','right'].includes(v)?v:'stereo'); while(placement.length<2) placement.push('stereo');
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
function signal(rssi=0){const n=!rssi?0:rssi>=-60?4:rssi>=-70?3:rssi>=-80?2:rssi?1:0;return `<span class="signal${n<=1?' warn':''}">${[1,2,3,4].map(i=>`<i class="${i<=n?'on':''}"></i>`).join('')}</span>`}
function card(inner,cls='',key=''){return `<div class="card ${cls}" ${key?`data-key="${attr(key)}"`:''}>${inner}</div>`}
function section(title,inner,aside=''){return `<section class="section"><div class="section-head"><span>${esc(title)}</span>${aside}</div>${inner}</section>`}

// Icons and connector states follow the approved design language.
// They are presentation only: no control, role or data path depends on them.
const ICON={phone:'M7 2h10a2 2 0 012 2v16a2 2 0 01-2 2H7a2 2 0 01-2-2V4a2 2 0 012-2zM11 18h2',computer:'M2 4h20v12H2zM8 20h8',headphones:'M3 18v-6a9 9 0 0118 0v6M3 14h3v6H3zM18 14h3v6h-3z',speaker:'M6 2h12v20H6zM12 8h.01M12 15m-3 0a3 3 0 106 0 3 3 0 10-6 0',unknown:'M12 22a10 10 0 100-20 10 10 0 000 20zM9.5 9a2.5 2.5 0 015 0c0 2-2.5 2-2.5 4M12 17h.01'};
const SVG={speaker:'<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2 6h3l4-3v10l-4-3H2z"></path><path d="M11 6a3 3 0 010 4"></path></svg>',device:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7 2h10a2 2 0 012 2v16a2 2 0 01-2 2H7a2 2 0 01-2-2V4a2 2 0 012-2zM11 18h2"></path></svg>',warning:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 9v4M12 17h.01M10.3 3.9L1.8 18.6A2 2 0 003.5 21.6h17a2 2 0 001.7-3L13.7 3.9a2 2 0 00-3.4 0z"></path></svg>',logout:'<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 21H5a2 2 0 01-2-2V5a2 2 0 012-2h4M16 17l5-5-5-5M21 12H9"></path></svg>'};
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
      <div class="top-status">${dot(model.realtime==='connected'?'connected':'warning')}<span>${esc(s.system.hostname)}</span></div><button class="btn ghost signout" data-action="logout" title="Sign out" aria-label="Sign out">${SVG.logout}<span class="btn-label">Sign out</span></button></header>
    <main data-key="page-${model.route}">${page}</main>
    <nav class="bottom-tabs">${routes.map(r=>`<button data-route="${r}" class="${r===model.route?'active':''}">${r}</button>`).join('')}</nav>
  </div>`, `shell-${model.route}`);
  alignConnectors();
}

function healthStrip(){const h=model.state.health;return `<div class="health-strip">
  <button data-route="Network">${dot(h.wifi.state)}<small>Wi‑Fi</small><span>${esc(h.wifi.value)}</span></button>
  <button data-route="Devices">${dot(h.bluetooth.state)}<small>Bluetooth</small><span>${esc(h.bluetooth.value)}</span></button>
  <button data-route="Audio">${dot(h.audio.state)}<small>Audio</small><span>${esc(h.audio.value)}</span></button></div>`}
function getDevice(addr){const a=String(addr||'').toUpperCase();return model.state.devices.find(d=>String(d.addr||'').toUpperCase()===a)}
function assignedAction(d){
  if(!d)return '';
  const act=d.connected?'disconnect':'connect';
  return btn(d.connected?'Disconnect':d.status==='connecting'?'Cancel':d.status==='error'?'Retry':'Connect',`bt-action:${act}:${d.addr}`,'secondary',model.btBusy?'disabled':'');
}
function autoToggle(d){
  if(!d)return '';
  return `<button class="auto-toggle ${d.autoConnect?'on':''}" data-bt-auto="${attr(d.addr)}" data-auto-enabled="${d.autoConnect?'1':'0'}" ${model.btBusy?'disabled':''}><span>Auto</span><i><b></b></i></button>`;
}
function nodeCard(label,d,index,m,output=false){
  if(!d)return card(`<small class="micro-label">${label}</small><span class="empty-icon">${SVG.device}</span><b>${output?(index===1?'Add second output':'No output'):index===1?'Add second source':'Add a source'}</b><span>${output?'Pair or assign a Bluetooth headset or speaker.':'Pair a phone or computer and assign it to this input.'}</span>${btn('Add device','go-devices')}`,'empty-slot node-card');
  const connected=d.connected, state=connected?'connected':d.status==='connecting'?'connecting':d.status==='error'?'error':'disconnected';
  const btVolume=Math.max(0,Math.min(100,model.pendingVolumes[d.addr]?.value ?? (d.volumeKnown?finite(d.volume,100):100)));
  if(d.volumeKnown&&btVolume>0) model.btVolumeMemory[d.addr]=btVolume;
  const btMuted=d.volumeKnown&&btVolume===0;
  const btControl=d.connected&&d.volumeKnown;
  return card(`<div class="device-title"><div><small class="micro-label">${label}</small><b title="${attr(d.name)}">${esc(d.name)}</b></div>${pill(state,connected?'Connected':d.status==='connecting'?'Connecting…':d.status==='error'?'Connection failed':'Disconnected')}</div>
    <div class="node-meta"><span>${signal(d.rssi)} ${d.rssi?`${d.rssi} dBm`:'Signal —'}</span><span>${esc(d.codec||'—')} · ${fmtRate(d.rate)}</span></div>
    ${d.latencyMs?`<div class="node-latency"><span>Transport latency</span><span>${d.latencyMs} ms</span></div>`:''}
    <small class="node-volume-label">Bluetooth volume</small>
    <div class="node-level"><span class="speaker-glyph">${SVG.speaker}</span><input data-bt-volume="${attr(d.addr)}" type="range" min="0" max="100" step="1" value="${btVolume}" ${btControl?'':'disabled'}><output data-bt-volume-output="${attr(d.addr)}">${d.volumeKnown?`${btVolume}%`:'—'}</output></div>
    <button class="btn node-mute ${btMuted?'primary':''}" data-bt-mute="${attr(d.addr)}" ${btControl?'':'disabled'}>${btMuted?'Unmute':'Mute'}</button>
    ${d.reason?`<p class="node-reason">${esc(d.reason)}</p>`:''}
    <div class="node-actions">${assignedAction(d)}${autoToggle(d)}</div>
    ${!output?`<details class="receiver-details"><summary>Receiver settings · ${esc(d.backend||'not connected')}</summary>${d.backend==='bluealsa'?`<label>SBC maximum (reconnects this receiver)<select data-secondary-cap="${attr(d.addr)}">${[35,53,64,250].map(n=>`<option value="${n}" ${model.state.audio.secondarySbcMaxBitpool===n?'selected':''}>${n}${n===35?' · Compatibility':''}</option>`).join('')}</select></label><small>Negotiated maximum: ${d.sbcMaxBitpool||'unknown'}. This cap belongs to the BlueALSA receiver, not an independent cap on both inputs.</small>`:'<small>PipeWire receiver. Configurable SBC cap is available on the secondary BlueALSA receiver only.</small>'}</details>`:''}`,'node-card',`${output?'output':'input'}-${index}`);
}
function mergeConnector(a,b){
  return `<div class="connector-svg merge" data-conn="merge"><svg viewBox="0 0 48 400" preserveAspectRatio="none" aria-hidden="true"><path class="${connClass(a)}" d="M0 97 C 24 97, 24 200, 48 200"/><path class="${connClass(b)}" d="M0 303 C 24 303, 24 200, 48 200"/><circle cx="45" cy="200" r="2.5"/></svg></div>`;
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
  if(merge&&ins.length===2){
    const paths=merge.querySelectorAll('path');
    ins.forEach((card,i)=>{
      if(!paths[i])return;
      const cy=+vy(card).toFixed(1);
      paths[i].setAttribute('d',`M0 ${cy} C 24 ${cy}, 24 ${my}, 48 ${my}`);
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
  const s=model.state,d1=getDevice(s.slots.inputs[0]),d2=getDevice(s.slots.inputs[1]),outs=s.slots.outputs.map(getDevice);
  if(!model.localMixer)model.localMixer=structuredClone(s.mixer);
  const m=model.localMixer;
  return `<div class="page dashboard">${healthStrip()}${model.mixerError?`<div class="warning">${esc(model.mixerError)}${btn('Retry mixer save','mixer-retry')}</div>`:''}${s.health.wifi.warning?`<div class="warning"><span class="warn-icon">${SVG.warning}</span><span class="warn-text">2.4 GHz Wi‑Fi competes with Bluetooth audio. 5 GHz is recommended for multiple streams.</span>${btn('Network','go-network','ghost')}</div>`:''}
    <div class="path-map">${pill(d1?.connected?'connected':'disconnected',d1?.name||'Input 1')}<span>+</span>${pill(d2?.connected?'connected':'disconnected',d2?.name||'Input 2')}<span>→</span>${outs.filter(Boolean).map(o=>pill(o.connected?'connected':'disconnected',o.name||'Output')).join('')}</div>
    <div class="signal-rail legacy-rail"><div class="input-column">${nodeCard('Input 1',d1,0,m)}${nodeCard('Input 2',d2,1,m)}</div>${mergeConnector(d1,d2)}
    ${card(`<div class="mixer-head"><div><small>Mixer</small><b>Headroom ${m.headroomDb} dB</b></div>${btn('Advanced','go-audio','ghost')}</div>
    ${[0,1].map(i=>`<div class="channel"><div class="channel-top"><span>Input ${i+1}</span><input data-mix-gain="${i}" type="range" min="-30" max="6" step="1" value="${m.gains[i]||0}"><output data-mix-output="${i}">${m.gains[i]||0} dB</output><button class="btn ${m.mutes[i]?'primary':'ghost'}" data-mute="${i}">${m.mutes[i]?'Unmute':'Mute'}</button></div><div class="channel-bottom"><small>Play on</small><div class="segments">${[['stereo','Both'],['left','L'],['right','R']].map(([v,l])=>`<button data-place="${i}:${v}" class="${m.placement[i]===v?'active':''}">${l}</button>`).join('')}</div><small>${m.placement[i]==='stereo'?'Stereo':m.placement[i]==='left'?'Mono to left ear':'Mono to right ear'}</small></div></div>`).join('')}
    <div class="master"><span>Master</span><input id="master-gain" type="range" min="-30" max="6" value="${m.master}"><output id="master-output">${m.master} dB</output><button class="btn ${m.masterMute?'primary':'ghost'}" data-action="master-mute">${m.masterMute?'Unmute':'Mute'}</button></div>`,'mixer')}
    ${outConnector(outs)}<div class="output-column">${[0,1].map(i=>nodeCard('Output '+(i+1),outs[i],i,m,true)).join('')}</div></div></div>`;
}

function deviceCard(d){
  const caps=asArray(d.caps), assigned=roleLabel(d.role), busy=model.btBusy;
  const kindLabel=d.kind==='source'?'Sends audio':d.kind==='output'?'Plays audio':d.kind==='audio-bidirectional'?'Sends and plays audio':(d.paired?'Bluetooth device':'Type unknown');
  const supportedRoles=new Set(['',...(caps.includes('sends_audio')?['in1','in2']:[]),...(caps.includes('plays_audio')?['out1','out2']:[])]); const staleRole=d.role&&!supportedRoles.has(d.role)?`<option value="${attr(d.role)}" selected>${esc(roleLabel(d.role))} (device missing)</option>`:''; const roleOptions=`<option value="" ${!d.role?'selected':''}>Unassigned</option>${staleRole}${caps.includes('sends_audio')?`<option value="in1" ${d.role==='in1'?'selected':''}>Input 1</option><option value="in2" ${d.role==='in2'?'selected':''}>Input 2</option>`:''}${caps.includes('plays_audio')?`<option value="out1" ${d.role==='out1'?'selected':''}>Output 1</option><option value="out2" ${d.role==='out2'?'selected':''}>Output 2</option>`:''}`;
  const actions=[];
  if(!d.paired) actions.push(btn('Pair',`bt-action:pair:${d.addr}`,'secondary',busy?'disabled':''));
  if(d.paired) actions.push(btn(d.connected?'Disconnect':'Connect',`bt-action:${d.connected?'disconnect':'connect'}:${d.addr}`,'secondary',busy?'disabled':''));
  if(d.paired) actions.push(btn('Forget',`bt-action:forget:${d.addr}`,'ghost',busy?'disabled':''));
  return card(`<div class="device-head"><span class="device-icon">${iconFor(d)}</span><div class="device-title"><div><small class="micro-label">${esc(kindLabel)}</small><b title="${attr(d.name)}">${esc(d.name)}</b></div>${pill(d.connected?'connected':'disconnected',d.connected?'Connected':d.paired?'Paired':'Available')}</div></div><div class="meta-row"><span>${esc(d.addr||'Unknown address')}</span><span>${signal(d.rssi)}${d.rssi||'—'}</span></div>
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
  <div class="pairbar"><div><b>${s.pairing.active?`Pairing mode · ${count}s`:'Pairing mode is off'}</b><span>${s.pairing.active?`Open Bluetooth settings on your phone or computer and choose ${esc(s.system.btName)}.`:'Enable pairing only when adding a device.'}</span></div>${btn(s.pairing.scanning?'Scanning…':'Scan','bt-scan','secondary',s.pairing.scanning||model.btBusy?'disabled':'')}${btn(s.pairing.active?'Stop pairing':'Start pairing','bt-pairing',s.pairing.active?'secondary':'primary',model.btBusy?'disabled':'')}</div>
  ${s.devices.length?`${assigned.length?section('Assigned',grid(assigned),head('Assigned',assigned)):''}${paired.length?section('Paired, not assigned',grid(paired),head('Paired',paired)):''}${found.length?section('Discovered, not paired',grid(found),head('Discovered',found)):''}`:card(`<span class="empty-icon">${SVG.device}</span><b>No Bluetooth devices saved</b><p>Start pairing or scan to add an input or output.</p>`,'empty-slot')}</div>`}

function networkPage(){const s=model.state,w=s.wifi;return `<div class="page"><div class="page-title"><div><h1>Network</h1><p>5 GHz is recommended because Bluetooth audio also uses the 2.4 GHz spectrum.</p></div>${btn('Scan networks','wifi-scan')}</div>${w.apply?`<div class="apply-banner ${attr(w.apply.state)}"><div><b>${w.apply.state==='verifying'?'Confirm this network':esc(w.apply.state)}</b><span>${esc(w.apply.message||'')}</span></div>${w.apply.state==='verifying'?btn('Confirm','wifi-confirm','primary'):''}</div>`:''}
  <div class="network-grid">${card(`<div class="device-title"><div><small>Connected network</small><b>${esc(w.ssid||'Not connected')}</b></div>${pill(w.ssid?(w.band==='2.4 GHz'?'warning':'connected'):'error',w.band)}</div><div class="stats"><div><small>Signal</small><b>${signal(w.rssi)} ${w.rssi||'—'} dBm</b></div><div><small>Channel</small><b>${w.channel||'—'} · ${w.freq||'—'} MHz</b></div><div><small>IP address</small><b>${esc(w.ip||'—')}</b></div><div><small>Reachable at</small><b>${esc(s.system.mdns)}</b></div></div>${w.band==='2.4 GHz'?'<div class="warning compact">2.4 GHz can cause Bluetooth dropouts under multi-stream load.</div>':''}`)}
  ${card(`<div class="section-head"><span>Other networks</span><small>${w.networks.length} found</small></div><div class="network-list">${w.networks.map(n=>`<div class="network-row"><button class="network-main" data-wifi-select="${attr(n.bssid)}">${signal(n.rssi)}<span>${esc(n.ssid)}</span><em>${esc(n.band)}</em><small>${n.rssi} dBm</small></button>${model.joinBssid===n.bssid?`<div class="join-form">${n.secure?'<input id="join-password" type="password" placeholder="Wi‑Fi password">':''}<p>The hub arms a 60-second rollback before switching networks.</p><button class="btn primary" data-wifi-join="${attr(n.bssid)}">Join ${esc(n.ssid)}</button></div>`:''}</div>`).join('')}</div>`)}</div></div>`}

const presets={low:{preset:'low',preferredRate:48000,allowedRates:[44100,48000],quantum:1024,bluealsaPeriodUs:50000,bluealsaBufferUs:200000,resampler:'auto',codecPolicy:'compatibility'},balanced:{preset:'balanced',preferredRate:48000,allowedRates:[44100,48000],quantum:2048,bluealsaPeriodUs:100000,bluealsaBufferUs:500000,resampler:'auto',codecPolicy:'compatibility'},stable:{preset:'stable',preferredRate:48000,allowedRates:[44100,48000],quantum:4096,bluealsaPeriodUs:100000,bluealsaBufferUs:500000,resampler:'auto',codecPolicy:'compatibility'}};
function audioPage(){const s=model.state;if(!model.localAudio)model.localAudio=structuredClone(s.audio);const a=model.localAudio;const dirty=JSON.stringify(a)!==JSON.stringify(s.audio);return `<div class="page audio-page"><div class="page-title"><div><h1>Audio</h1><p>Change latency, sampling and stability settings. Applying restarts the audio graph.</p></div></div>
  ${section('Presets',`<div class="preset-row">${[['low','Low latency'],['balanced','Balanced'],['stable','Maximum stability']].map(([k,l])=>`<button data-preset="${k}" class="${a.preset===k?'active':''}"><b>${l}</b><small>${k==='low'?'q1024 · 50/200 ms':k==='balanced'?'q2048 · 100/500 ms':'q4096 · 100/500 ms'}</small></button>`).join('')}</div>`)}
  <div class="settings-grid">${card(`<h2>Sampling</h2><label>Preferred graph rate<select data-audio="preferredRate"><option value="44100" ${a.preferredRate===44100?'selected':''}>44.1 kHz</option><option value="48000" ${a.preferredRate===48000?'selected':''}>48 kHz</option><option value="96000" ${a.preferredRate===96000?'selected':''}>96 kHz</option></select></label><label class="check"><input data-rate="44100" type="checkbox" ${a.allowedRates.includes(44100)?'checked':''}>Allow 44.1 kHz</label><label class="check"><input data-rate="48000" type="checkbox" ${a.allowedRates.includes(48000)?'checked':''}>Allow 48 kHz</label>`)}
  ${card(`<h2>Buffers & latency</h2><label>PipeWire quantum<input data-audio="quantum" type="number" min="256" max="8192" step="256" value="${a.quantum}"></label><label>BlueALSA period (µs)<input data-audio="bluealsaPeriodUs" type="number" step="10000" value="${a.bluealsaPeriodUs}"></label><label>BlueALSA buffer (µs)<input data-audio="bluealsaBufferUs" type="number" step="10000" value="${a.bluealsaBufferUs}"></label>`)}
  ${card(`<h2>Codec policy</h2><label>Bluetooth codec<select data-audio="codecPolicy"><option value="compatibility" ${a.codecPolicy==='compatibility'?'selected':''}>Compatibility (SBC)</option><option value="quality" ${a.codecPolicy==='quality'?'selected':''}>Quality when available</option></select></label><p class="settings-note">Resampling is left to PipeWire. The Debian BlueALSA 4.3.1 player has no adaptive-resampler control, so there is no resampler setting here.</p>`)}
  ${card(`<h2>Secondary receiver</h2><label>SBC maximum bitpool<select data-audio="secondarySbcMaxBitpool">${[35,53,64,250].map(n=>`<option value="${n}" ${a.secondarySbcMaxBitpool===n?'selected':''}>${n}${n===35?' · Compatibility':''}</option>`).join('')}</select></label><p>Applies to the BlueALSA receiver only. A source must reconnect to negotiate a new cap. Primary PipeWire input is unchanged.</p>`)}
  ${delayCard(a)}</div>${dirty?`<div class="applybar"><span>Audio settings have unapplied changes.</span>${btn('Revert','audio-revert')}${btn('Apply & restart audio','audio-apply','primary')}</div>`:''}</div>`}

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
    <div class="delay-scope"><span class="pill">${dot(dotState)}${esc(label)}</span><span>Input 2 only — the BlueALSA receiver. Input 1 runs on PipeWire, which has no supported way for OpenAudioHub to write this value, so it is not affected.</span></div>
    <label>Advertised total sink delay (ms)<input data-audio="secondaryAdvertisedDelayMs" type="number" min="0" max="2000" step="10" value="${finite(a.secondaryAdvertisedDelayMs,0)}"><small>The <b>total</b> reported latency from source to ears, not extra buffering. 0 keeps the engine default.</small></label>
    <div class="delay-actions">${btn('Apply to receiver','delay-apply','primary')}${btn('Reset to 0','delay-reset')}</div>
    <dl class="delay-status">${rows.map(([k,v])=>`<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('')}</dl>
    ${r.detail?`<p class="settings-note">${esc(r.detail)}</p>`:''}
    <details class="calibration"><summary>How to calibrate this value</summary><ol><li>Play the same clip on the source and measure the audio-to-video offset at your ears — that measurement is the total.</li><li>Enter that total in milliseconds and press <b>Apply to receiver</b>.</li><li>The source must reconnect before it can adopt a new report. Watch the status change from Pending confirmation.</li><li>Measure again. BlueZ accepting the report does not prove the source corrected its video; only re-measuring does.</li></ol><p>There is no separate offset control: a residual lip-sync error is added into this total, not applied on top of it. A headphone or output latency you measured is already inside the total — adding it again would double-count it.</p></details>`,'delay-card');
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

function systemPage(){const s=model.state.system;return `<div class="page"><div class="page-title"><div><h1>System</h1><p>Identity, software information and maintenance.</p></div></div><div class="settings-grid">${card(`<h2>Identity</h2><label>Hostname<input id="system-hostname" value="${attr(s.hostname)}"><small>${esc(s.hostname)}.local</small></label><label>Bluetooth name<input id="system-btname" value="${attr(s.btName)}"></label>${btn('Save identity','identity-save','primary')}`)}
  ${card(`<h2>About</h2><dl><dt>OpenAudioHub</dt><dd>${esc(s.version)}</dd><dt>Operating system</dt><dd>${esc(s.os)}</dd><dt>Uptime</dt><dd>${esc(s.uptime)}</dd><dt>Current time</dt><dd>${esc(new Date(s.time).toLocaleString())}</dd></dl>`)}
  ${card(`<h2>Maintenance</h2><div class="stack-actions">${btn('Restart audio graph','system:restart-audio')}<a class="btn secondary" href="/api/system/backup">Download configuration backup</a>${btn('Reboot hub','system:reboot','danger')}</div>`)}
  ${card(`<h2>Change password</h2><label>Current password<input id="pw-current" type="password"></label><label>New password<input id="pw-new" type="password"></label><label>Repeat new password<input id="pw-again" type="password"></label>${btn('Change password','password-change')}`)}</div></div>`}

function diagnosticsPage(){const d=model.diagnostics||model.state.diagnostics;if(!d)return `<div class="page"><div class="page-title"><h1>Diagnostics</h1></div>${btn('Load diagnostics','diag-refresh')}</div>`;return `<div class="page"><div class="page-title"><div><h1>Diagnostics</h1><p>System health, transports and recent logs.</p></div><div>${btn('Refresh','diag-refresh')} ${btn('Copy diagnostics','diag-copy','primary')}</div></div><div class="diag-cards">${card('<small>CPU</small><b>'+d.cpuPercent.toFixed(1)+'%</b>')}${card('<small>Memory</small><b>'+d.memPercent.toFixed(1)+'%</b>')}${card(`<small>Temperature</small><b>${d.tempC?d.tempC.toFixed(1)+' °C':'—'}</b>`)}${card('<small>PipeWire errors</small><b>'+d.xruns+'</b>')}</div>
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
  if(e.target.dataset.audio){const k=e.target.dataset.audio;model.localAudio[k]=e.target.type==='checkbox'?e.target.checked:(e.target.type==='number'||k==='preferredRate'||k==='secondarySbcMaxBitpool'?+e.target.value:e.target.value);model.localAudio.preset='custom';render();}
  if(e.target.dataset.rate){const n=+e.target.dataset.rate;model.localAudio.allowedRates=e.target.checked?[...new Set([...model.localAudio.allowedRates,n])]:model.localAudio.allowedRates.filter(x=>x!==n);model.localAudio.preset='custom';render();}
});
app.addEventListener('change',async e=>{
  if(e.target.dataset.secondaryCap!==undefined){
    const control=e.target,cfg=structuredClone(model.state.audio);cfg.secondarySbcMaxBitpool=+control.value;
    if(!confirm('Apply this SBC maximum and reconnect the secondary receiver?')){control.value=String(model.state.audio.secondarySbcMaxBitpool);releaseControl(control);render();return;}
    try{await post('/api/audio',cfg);model.state.audio=cfg;model.localAudio=structuredClone(cfg);toast('Receiver settings saved; reconnecting secondary source.');}
    catch(x){control.value=String(model.state.audio.secondarySbcMaxBitpool);toast(x.message,'error');}finally{releaseControl(control);render();}return;
  }
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

async function loadDiagnostics(){try{const s=normalizeState(await api('/api/diagnostics'));model.diagnostics=normalizeDiagnostics(s.diagnostics)}catch(e){toast(e.message,'error')}}
loadPublic();
