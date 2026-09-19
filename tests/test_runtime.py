import importlib.util,json,os,subprocess,tempfile,unittest
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('patcher',ROOT/'patches/bluealsa-receiver.py')
patcher=importlib.util.module_from_spec(spec);spec.loader.exec_module(patcher)
class RuntimeTests(unittest.TestCase):
 def run_values(self,raw):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/'audio.json';p.write_text(raw)
   return subprocess.run(['python3',str(ROOT/'packaging/scripts/audio-values.py'),str(p)],capture_output=True,text=True)
 def test_defaults(self):
  r=self.run_values('{}');self.assertEqual(r.returncode,0);self.assertEqual(r.stdout.split(),['100000','500000','35','0'])
 def test_invalid_values_and_types(self):
  for obj in [None,[],{'secondarySbcMaxBitpool':True},{'secondarySbcMaxBitpool':'35; touch /tmp/bad'},{'secondarySbcMaxBitpool':36},{'secondaryAdvertisedDelayMs':-1},{'bluealsaPeriodUs':75000},{'bluealsaBufferUs':100000}]:
   r=self.run_values(json.dumps(obj));self.assertEqual(r.returncode,78,(obj,r));self.assertNotIn('Traceback',r.stderr)
  self.assertEqual(self.run_values('{bad').returncode,78)
 def test_secret_not_read_by_player(self):
  bridge=(ROOT/'packaging/scripts/bluealsa-bridge.sh').read_text()
  self.assertNotIn('/config.json',bridge);self.assertIn('audio-values.py',bridge)
  unit=(ROOT/'packaging/systemd/openaudiohub-bluealsa-bridge.service.in').read_text()
  self.assertNotIn('openaudiohub-audio-tuning',unit);self.assertIn('RestartPreventExitStatus=73 78',unit)
 def test_legacy_user_service_is_retired(self):
  s=(ROOT/'scripts/install.sh').read_text()
  self.assertIn('openaudiohub-bluealsa-aplay.service',s)
  self.assertLess(s.index('systemctl --user disable --now'),s.index('echo "[2/9]'))
  self.assertIn('chmod 0600 /etc/openaudiohub/config.json',s)
 def test_unprivileged_reader_can_read_public_not_private(self):
  if os.geteuid()!=0:self.skipTest('permission boundary test requires root test runner')
  with tempfile.TemporaryDirectory() as d:
   os.chmod(d,0o755)
   public=Path(d)/'audio.json';public.write_text('{}');os.chmod(public,0o644)
   private=Path(d)/'config.json';private.write_text('{"passwordHash":"secret"}');os.chmod(private,0o600)
   helper=(ROOT/'packaging/scripts/audio-values.py').read_text()
   def demote():os.setgroups([]);os.setgid(65534);os.setuid(65534)
   r=subprocess.run(['python3','-c',helper,str(public)],capture_output=True,text=True,preexec_fn=demote)
   self.assertEqual(r.returncode,0,r.stderr)
   r=subprocess.run(['python3','-c','import sys;open(sys.argv[1]).read()',str(private)],capture_output=True,text=True,preexec_fn=demote)
   self.assertNotEqual(r.returncode,0);self.assertIn('PermissionError',r.stderr)
 def test_patch_changes_sink_not_shared_maximum(self):
  # Structural fixture, not a substitute for full upstream build.
  text='''#include <stdint.h>\n#include "ba-config.h"\n#define SBC_MAX_BITPOOL 250\nstruct a2dp_sep a2dp_sbc_source = {\n .max_bitpool = SBC_MAX_BITPOOL,\n};\nstatic int a2dp_sbc_sink_transport_start(struct ba_transport *t) {\n return 0;\n}\nstruct a2dp_sep a2dp_sbc_sink = {\n .max_bitpool = SBC_MAX_BITPOOL,\n\t.configuration_select = a2dp_sbc_configuration_select,\n};\n'''
  out=patcher.patch(text);self.assertIn('#define SBC_MAX_BITPOOL 250',out);self.assertIn('.init = oah_sbc_sink_init',out)
  self.assertIn('g_variant_new_uint16(ms * 10)',out)
  self.assertEqual(out.count('.init = oah_sbc_sink_init'),1)
  with self.assertRaises(ValueError):patcher.patch(out)
  with self.assertRaises(ValueError):patcher.patch('unexpected source')
if __name__=='__main__':unittest.main(verbosity=2)
