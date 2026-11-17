import json,pathlib,subprocess,sys,tempfile,unittest
SCRIPT=pathlib.Path(__file__).with_name('pair_stream_traces.py')
class PairTraceTests(unittest.TestCase):
 def test_pairing_does_not_treat_completion_interval_as_unblocked_write(self):
  utc='2026-11-17T16:00:00Z'
  def e(stage,mono,**kw):return dict(stage=stage,mono_us=mono,utc=utc,**kw)
  host=[e('encode.total',10000,frame=1,value=1007),e('produce.frame',10000,frame=1,value=10),e('encode.total',100000,frame=2,value=2007),e('produce.frame',100000,frame=2,value=100),e('rtp.send_write',1000000,frame=5900,duration_us=580000),e('rtp.send',1000000,frame=5900,value=1,result=7),e('rtp.send',1000508,frame=14000,value=2,result=7),e('transport.write_sample',1000508,frame=100,duration_us=508),e('relay.write',1000508)]
  # Viewer mono is a different epoch; no absolute latency can be inferred.
  viewer=[e('rtp.packet',9000000,frame=5900,value=1,result=7),e('rtp.sample',9000100,frame=5900,value=1001),e('render.new_submit',9000100,frame=5900),e('rtp.packet',9600000,frame=14000,value=2,result=7),e('rtp.sample',9600100,frame=14000,value=2001),e('render.new_submit',9600100,frame=14000)]
  with tempfile.TemporaryDirectory() as d:
   root=pathlib.Path(d)
   for name,rows in [('h',host),('v',viewer)]: (root/name).write_text(''.join(json.dumps(r)+'\n' for r in rows),encoding='utf-8')
   out=root/'out';subprocess.run([sys.executable,str(SCRIPT),str(root/'h'),str(root/'v'),'--out',str(out)],check=True,capture_output=True)
   r=json.loads(out.read_text(encoding='utf-8'))
  self.assertEqual(r['timestamp_offset'],5000);self.assertEqual(r['matched_samples'],2)
  self.assertEqual(r['packet_gaps'][0]['viewer_gap_ms'],600)
  self.assertEqual(r['packet_gaps'][0]['sender_completion_interval_ms'],.508)
  self.assertEqual(r['stalls'][0]['host_summary']['rtp.send_write']['duration_ms']['max'],580)
  self.assertEqual(r['host_durations_ms']['relay.write']['p50'],0)
  self.assertEqual(r['stalls'][0]['host_context_end_us'],1000508)
if __name__=='__main__':unittest.main()
