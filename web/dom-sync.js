// Preserve native control identity, focus, and edits across telemetry refreshes.
// No virtual-DOM runtime, polling timers, or external dependency is required.
const dirty = new WeakSet();
let pointerDown = false;
export function installInteractionGuards(root, onIdle) {
  const control = e => e.target instanceof Element && e.target.matches('input,select,textarea');
  root.addEventListener('input', e => { if(control(e)) dirty.add(e.target); }, true);
  root.addEventListener('change', e => { if(control(e)) dirty.add(e.target); }, true);
  root.addEventListener('pointerdown', e => { if(control(e)) pointerDown = true; }, true);
  for(const type of ['pointerup','pointercancel']) document.addEventListener(type,()=>{pointerDown=false;onIdle();},true);
  root.addEventListener('focusout',()=>setTimeout(onIdle,0),true);
}
export function interacting(root) {
  return pointerDown || (root.contains(document.activeElement) && document.activeElement.matches('input,select,textarea'));
}
export function releaseControl(el) { if(el) dirty.delete(el); }
export function releaseControls(root) { for(const el of root.querySelectorAll('input,select,textarea')) dirty.delete(el); }
function key(n) {
  if(n.nodeType!==1) return null;
  for(const attr of ['data-key','id','data-audio','data-rate','data-role-addr','data-bt-volume','data-mix-gain','data-mix-output','data-bt-volume-output','data-secondary-cap','data-route','data-action','data-preset','data-place','data-mute','data-bt-auto','data-bt-mute','data-wifi-select']) {
    if(n.hasAttribute(attr)) return n.tagName+'|'+attr+'|'+n.getAttribute(attr);
  }
  return null;
}
function same(a,b) { return a.nodeType===b.nodeType && (a.nodeType!==1 || (a.tagName===b.tagName && key(a)===key(b))); }
function sync(old,next) {
  if(old.nodeType!==1) { if(old.nodeValue!==next.nodeValue)old.nodeValue=next.nodeValue;return; }
  const form=old.matches('input,select,textarea');
  const protect=form && (dirty.has(old) || old===document.activeElement || pointerDown);
  // Do not rebuild a focused select's option subtree: doing so closes its popup.
  if(old.tagName==='SELECT' && protect) return;
  const keepOpen=old.tagName==='DETAILS' && old.open;
  for(const attr of [...old.attributes]) {
    if(protect&&['value','checked','selected'].includes(attr.name)) continue;
    if(attr.name==='open'&&old.tagName==='DETAILS') continue;
    if(!next.hasAttribute(attr.name)) old.removeAttribute(attr.name);
  }
  for(const attr of next.attributes) {
    if(protect&&['value','checked','selected'].includes(attr.name)) continue;
    if(attr.name==='open'&&old.tagName==='DETAILS') continue;
    if(old.getAttribute(attr.name)!==attr.value)old.setAttribute(attr.name,attr.value);
  }
  if(old.tagName!=='INPUT'&&old.tagName!=='TEXTAREA') children(old,next);
  if(form&&!protect) {
    if(old.value!==next.value) old.value=next.value;
    if(old.tagName==='INPUT') old.checked=next.checked;
  }
  if(old.tagName==='DETAILS') old.open=keepOpen;
}
function children(old,next) {
  let cursor=old.firstChild;
  for(const wanted of [...next.childNodes]) {
    let match=cursor;
    if(!match||!same(match,wanted)) {
      const k=key(wanted);
      match=k?[...old.childNodes].find(n=>same(n,wanted)):null;
      if(!match)match=wanted.cloneNode(true);
      old.insertBefore(match,cursor);
    }
    sync(match,wanted);
    cursor=match.nextSibling;
  }
  while(cursor) { const remove=cursor;cursor=cursor.nextSibling;remove.remove(); }
}
let scope='';
export function patchHTML(root, html, nextScope) {
  const tpl=document.createElement('template');tpl.innerHTML=html;
  if(scope!==nextScope) { root.replaceChildren(tpl.content.cloneNode(true));scope=nextScope;return; }
  children(root,tpl.content);
}
