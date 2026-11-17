import json,pathlib,subprocess,sys,tempfile,unittest
SCRIPT=pathlib.Path(__file__).with_name('analyze_candidate_trace.py')
class CandidateAnalysisTests(unittest.TestCase):
 def analyze(self,events,warmup=0,seconds=2,tail=''):
  with tempfile.TemporaryDirectory() as d:
   source=pathlib.Path(d)/'trace.jsonl';out=pathlib.Path(d)/'out.json'
   source.write_text(''.join(json.dumps(e)+'\n' for e in events)+tail,encoding='utf-8')
   subprocess.run([sys.executable,str(SCRIPT),str(source),'--out',str(out),'--warmup',str(warmup),'--seconds',str(seconds)],check=True,capture_output=True)
   return json.loads(out.read_text(encoding='utf-8'))[0]
 def test_exact_warmup_window_and_boundary(self):
  rows=[dict(stage='decode.output',object=1,frame=i,mono_us=i*1000000) for i in range(5)]
  r=self.analyze(rows,warmup=1)
  self.assertEqual((r['window_start_us'],r['window_end_us'],r['streams'][0]['count'],r['streams'][0]['fps']),(1000000,3000000,2,1))
  self.assertTrue(r['complete_window'])
 def test_redraw_is_not_new_frame(self):
  rows=[dict(stage='decode.output',mono_us=0),dict(stage='render.new_submit',object=2,frame=1,mono_us=1000),dict(stage='render.redraw',object=2,frame=1,mono_us=200000),dict(stage='render.new_submit',object=2,frame=2,mono_us=701000),dict(stage='health',mono_us=2000000)]
  r=self.analyze(rows);render=next(s for s in r['streams'] if s['stage']=='render.new_submit')
  self.assertEqual(render['count'],2);self.assertEqual(render['thresholds']['500'],1)
  self.assertEqual([s['gap_ms'] for s in r['stalls'] if s['stage']=='render.new_submit'],[700])
 def test_partial_trace_is_not_complete_window(self):
  r=self.analyze([dict(stage='decode.output',mono_us=0),dict(stage='health',mono_us=1000000)],tail='{"stage":')
  self.assertFalse(r['complete_window']);self.assertEqual(r['seconds'],1);self.assertEqual(r['invalid_lines'],1)
if __name__=='__main__':unittest.main()
