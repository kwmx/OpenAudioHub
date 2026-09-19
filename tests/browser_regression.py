"""Real Chromium interaction tests with in-page mocked API/telemetry (offline; no HTTP or Bluetooth hardware)."""
import copy,json,threading,time,unittest
from functools import partial
from http.server import ThreadingHTTPServer,SimpleHTTPRequestHandler
from pathlib import Path
from playwright.sync_api import sync_playwright
ROOT=Path(__file__).resolve().parents[1]
STATE={
'revision':0,'slots':{'inputs':['AA:AA:AA:AA:AA:01','AA:AA:AA:AA:AA:02'],'outputs':['AA:AA:AA:AA:AA:03']},
'devices':[{'addr':f'AA:AA:AA:AA:AA:0{i}','name':n,'caps':['input' if i<3 else 'output'],'connected':True,'paired':True,'role':['in1','in2','out1'][i-1],'backend':'bluealsa' if i==2 else 'pipewire','volumeKnown':True,'volume':70,'codec':'SBC','rate':44100,'sbcMaxBitpool':35 if i==2 else 64} for i,n in [(1,'Phone'),(2,'Mac'),(3,'Headphones')]],
'mixer':{'gains':[0,0],'mutes':[False,False],'placement':['stereo','stereo'],'master':0,'masterMute':False,'headroomDb':-6,'limiter':False},
'audio':{'preset':'balanced','preferredRate':48000,'allowedRates':[44100,48000],'quantum':2048,'bluealsaPeriodUs':100000,'bluealsaBufferUs':500000,'resampler':'auto','codecPolicy':'compatibility','liveMeters':False,'secondarySbcMaxBitpool':35,'secondaryAdvertisedDelayMs':0},
'wifi':{'ssid':'Test-5G','band':'5 GHz','freq':5180},
'system':{'hostname':'openaudiohub','mdns':'openaudiohub.local','btName':'OpenAudioHub','version':'0.1.6-rc1','uptime':'1h'},
'health':{'wifi':{'state':'connected','value':'Test-5G · 5 GHz'},'bluetooth':{'state':'connected','value':'2 inputs · 1 output'},'audio':{'state':'connected','value':'Running'},'system':{'state':'connected','value':'Healthy'}},'pairing':{}}
class BrowserRegression(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  cls.pw=sync_playwright().start();cls.browser=cls.pw.chromium.launch(executable_path='/usr/bin/chromium',headless=True,args=['--no-sandbox'])
 @classmethod
 def tearDownClass(cls):cls.browser.close();cls.pw.stop()
 def setUp(self):
  self.page=self.browser.new_page(viewport={'width':1440,'height':1000})
  self.errors=[];self.page.on('pageerror',lambda e:self.errors.append(str(e)))
  self.page.set_content('<div id="app"></div><div id="toast"></div>')
  for css in ['tokens.css','styles.css']:self.page.add_style_tag(content=(ROOT/'web'/css).read_text())
  self.page.evaluate("s=>window.__server={state:s,writes:[],fail:false}",copy.deepcopy(STATE))
  self.page.evaluate('''()=>{
   window.EventSource=class {constructor(){this.listeners={};window.__es=this;setTimeout(()=>this.onopen?.(),0)}addEventListener(n,f){this.listeners[n]=f}close(){}};
   window.__emit=s=>window.__es.listeners.state({data:JSON.stringify(s)});
   window.fetch=async (path,opts={})=>{
    const api=window.__server;let result={};let status=200;
    if(!opts.method||opts.method==='GET'){
     if(path==='/api/public')result={authenticated:true,mdns:'openaudiohub.local'};
     else if(path==='/api/state')result=structuredClone(api.state);
    } else {
     await new Promise(r=>setTimeout(r,150));const body=JSON.parse(opts.body||'{}');api.writes.push([path,body]);
     if(api.fail){api.fail=false;result={error:'Simulated save failure'};status=503;}
     else {if(path==='/api/mixer')api.state.mixer=body;if(path==='/api/audio')api.state.audio=body;api.state.revision++;result={ok:true,revision:api.state.revision};}
    }
    return new Response(JSON.stringify(result),{status,headers:{'Content-Type':'application/json'}});
   };
  }''')
  js=(ROOT/'web/dom-sync.js').read_text().replace('export function','function')
  js+='\n'+(ROOT/'web/app.js').read_text().split('\n',1)[1]
  from base64 import b64encode
  for asset in (ROOT/'web/assets').glob('*.svg'):
   js=js.replace('/assets/'+asset.name,'data:image/svg+xml;base64,'+b64encode(asset.read_bytes()).decode())
  self.page.add_script_tag(type='module',content=js)
  self.page.wait_for_selector('[data-mix-gain="0"]')
 def tearDown(self):
  self.assertEqual(self.errors,[]);self.page.close()
 def emit(self,mut=None):
  s=self.page.evaluate("window.__server.state")
  if mut:mut(s)
  self.page.evaluate('s=>window.__emit(s)',s)
 def route(self,name):self.page.locator(f'.topbar [data-route="{name}"]').click()
 def test_dropdown_native_node_and_audio_draft_survive_updates(self):
  self.route('Audio');sel=self.page.locator('[data-audio="secondarySbcMaxBitpool"]');sel.evaluate('e=>window.savedSelect=e');sel.focus()
  self.page.keyboard.press('ArrowDown')
  value=sel.input_value()
  for _ in range(4):self.emit()
  self.assertTrue(sel.evaluate('e=>e===window.savedSelect'));self.assertEqual(sel.input_value(),value)
  self.page.keyboard.press('Escape');self.page.locator('[data-audio="quantum"]').fill('4096');self.page.locator('h1').click()
  self.emit();self.assertEqual(self.page.locator('[data-audio="quantum"]').input_value(),'4096')
  self.page.screenshot(path=str(ROOT/'tests/artifacts/audio-desktop.png'))
 def test_real_slider_drag_survives_telemetry(self):
  slider=self.page.locator('[data-mix-gain="0"]');slider.evaluate('e=>window.savedSlider=e');b=slider.bounding_box()
  self.page.mouse.move(b['x']+b['width']*.82,b['y']+b['height']/2);self.page.mouse.down();self.page.mouse.move(b['x']+b['width']*.3,b['y']+b['height']/2,steps=8)
  v=slider.input_value();self.emit();self.assertEqual(slider.input_value(),v);self.assertTrue(slider.evaluate('e=>e===window.savedSlider'))
  self.page.mouse.up();self.page.wait_for_timeout(300);self.page.locator('.mixer-head').click();self.emit()
  self.assertEqual(slider.input_value(),v);self.assertEqual(self.page.evaluate('window.__server.writes').pop()[1]['gains'][0],int(v))
 def test_rapid_saves_and_old_revision_cannot_reset_slider(self):
  self.page.evaluate('''()=>{const e=document.querySelector('[data-mix-gain="0"]');for(const v of [-8,-5,-2]){e.value=v;e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));}}''')
  self.page.wait_for_timeout(700)
  self.assertEqual([b['gains'][0] for p,b in self.page.evaluate('window.__server.writes') if p=='/api/mixer'],[-8,-5,-2])
  self.page.evaluate('s=>window.__emit(s)',copy.deepcopy(STATE))
  self.assertEqual(self.page.locator('[data-mix-gain="0"]').input_value(),'-2')
 def test_pending_bluetooth_volume_is_not_mixer_gain(self):
  self.page.evaluate('''()=>{const e=document.querySelector('[data-bt-volume="AA:AA:AA:AA:AA:02"]');e.value=43;e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));}''')
  self.page.wait_for_timeout(300);self.emit()
  self.assertEqual(self.page.locator('[data-bt-volume="AA:AA:AA:AA:AA:02"]').input_value(),'43')
  self.assertEqual(self.page.locator('[data-mix-gain="1"]').input_value(),'0')
 def test_identity_edit_survives_telemetry_and_selection(self):
  self.route('System');field=self.page.locator('#system-btname');field.fill('My revised hub');field.evaluate('e=>{e.setSelectionRange(3,10);window.savedField=e;}');self.emit()
  self.assertEqual(field.input_value(),'My revised hub');self.assertTrue(field.evaluate('e=>e===window.savedField'))
  self.assertEqual(field.evaluate('e=>[e.selectionStart,e.selectionEnd]'),[3,10])
 def test_failed_save_keeps_draft_and_retry(self):
  self.page.evaluate('window.__server.fail=true')
  self.page.evaluate('''()=>{const e=document.querySelector('[data-mix-gain="1"]');e.value=-9;e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));}''')
  self.page.wait_for_selector('[data-action="mixer-retry"]');self.emit();self.assertEqual(self.page.locator('[data-mix-gain="1"]').input_value(),'-9')
  self.page.locator('[data-action="mixer-retry"]').click();self.page.wait_for_timeout(300)
  self.assertEqual(self.page.evaluate('window.__server.state.mixer.gains[1]'),-9)
 def test_receiver_details_and_mobile_layout(self):
  self.page.locator('.receiver-details').nth(1).locator('summary').click();self.emit()
  self.assertTrue(self.page.locator('.receiver-details').nth(1).evaluate('e=>e.open'))
  self.page.screenshot(path=str(ROOT/'tests/artifacts/dashboard-desktop.png'))
  self.page.set_viewport_size({'width':390,'height':844});self.emit();self.page.screenshot(path=str(ROOT/'tests/artifacts/dashboard-mobile.png'))
  self.assertLessEqual(self.page.evaluate('document.documentElement.scrollWidth'),392)
if __name__=='__main__':unittest.main(verbosity=2)
