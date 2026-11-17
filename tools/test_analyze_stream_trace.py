import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

SCRIPT=pathlib.Path(__file__).with_name('analyze_stream_trace.py')
class TraceAnalysisTests(unittest.TestCase):
    def analyze(self,events):
        with tempfile.TemporaryDirectory() as directory:
            source=pathlib.Path(directory)/'trace.jsonl';target=pathlib.Path(directory)/'analysis.json'
            with source.open('w',encoding='utf-8') as f:
                for event in events:f.write(json.dumps(event)+'\n')
            subprocess.run([sys.executable,str(SCRIPT),str(source),'--out',str(target)],check=True,capture_output=True)
            return json.loads(target.read_text(encoding='utf-8'))['files'][0]
    def packets(self,numbers):
        return (dict(stage='rtp.packet',object=1,result=77,value=n%65536,frame=i*3000,mono_us=i*1000) for i,n in enumerate(numbers))
    def test_reorder_across_wrap(self):
        q=self.analyze(self.packets([65534,65536,65535,65537]))['rtp_quality']
        self.assertEqual((q['observed_gap_packets'],q['recovered_within_window'],q['unresolved_expired']),(1,1,0))
    def test_next_epoch_does_not_recover_old_gap(self):
        def numbers():
            yield 65534
            yield from range(65536,131072)
        q=self.analyze(self.packets(numbers()))['rtp_quality']
        self.assertEqual((q['observed_gap_packets'],q['recovered_within_window'],q['unresolved_expired']),(1,0,1))
    def test_repaint_is_not_new_frame(self):
        rows=[dict(stage='decode.output',object=2,frame=100,mono_us=900),dict(stage='render.present',object=2,frame=100,mono_us=1000),dict(stage='render.present',object=2,frame=100,mono_us=201000),dict(stage='decode.output',object=2,frame=200,mono_us=600900),dict(stage='render.present',object=2,frame=200,mono_us=601000)]
        result=self.analyze(rows)
        fresh=[s for s in result['stalls'] if s['stage']=='render.new_frame_interval']
        self.assertEqual([s['duration_us'] for s in fresh],[600000])
        self.assertFalse(any(s['stage']=='render.interval' for s in result['stalls']))
if __name__=='__main__':unittest.main()
