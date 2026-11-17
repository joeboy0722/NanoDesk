"""Candidate trace windows use each process's own monotonic clock."""
import argparse,json,glob,collections,math,pathlib
p=argparse.ArgumentParser();p.add_argument('files',nargs='+');p.add_argument('--out',required=True);p.add_argument('--warmup',type=float,default=10);p.add_argument('--seconds',type=float,default=180);a=p.parse_args()
def quant(v,q):return sorted(v)[max(0,math.ceil(len(v)*q)-1)] if v else None
def stats(v):return dict(n=len(v),p50_ms=quant(v,.5),p95_ms=quant(v,.95),p99_ms=quant(v,.99),max_ms=max(v,default=None))
reports=[]
for name in [v for pat in a.files for v in glob.glob(pat)]:
 events=[];invalid=0
 for line in pathlib.Path(name).open(encoding='utf-8'):
  try:events.append(json.loads(line))
  except json.JSONDecodeError:invalid+=1
 events.sort(key=lambda e:e.get('mono_us',0))
 anchor=next((e['mono_us'] for e in events if e['stage']=='decode.output'),None)
 if anchor is None:anchor=next((e['mono_us'] for e in events if e['stage']=='produce.frame'),0)
 start=anchor+int(a.warmup*1e6);end=min(start+int(a.seconds*1e6),max((e['mono_us'] for e in events),default=0));duration=max(0,end-start)/1e6
 rows=[e for e in events if start<=e['mono_us']<end];groups=collections.defaultdict(list)
 for e in rows:groups[(e['stage'],e.get('object',0))].append(e)
 streams=[];stalls=[]
 for (stage,obj),group in groups.items():
  if stage not in ('decode.output','render.new_submit','produce.frame'):continue
  timestamps=[e['mono_us'] for e in group];gaps=[(y-x)/1000 for x,y in zip(timestamps,timestamps[1:])]
  streams.append(dict(stage=stage,object=obj,count=len(group),fps=len(group)/duration if duration else 0,interval=stats(gaps),thresholds={str(t):sum(g>t for g in gaps) for t in (100,250,500,1000)}))
  for prev,e in zip(group,group[1:]):
   if e['mono_us']-prev['mono_us']>500000:
    interesting=('capture.','encode.','schedule.','idr.','video.recovery','viewer.queue','rtp.assembly','ui.poll','render.new_')
    context=[v for v in events if prev['mono_us']-100000<=v['mono_us']<=e['mono_us'] and v['stage'].startswith(interesting)]
    stalls.append(dict(stage=stage,object=obj,start_us=prev['mono_us'],end_us=e['mono_us'],gap_ms=(e['mono_us']-prev['mono_us'])/1000,events=context,attribution='pending paired host/viewer timeline'))
 summary={stage:dict(count=len(group),duration=stats([e.get('duration_us',0)/1000 for e in group])) for (stage,obj),group in groups.items() if stage in ('viewer.queue_age','capture.total','encode.total','schedule.budget_actual','rtp.assembly_ready','render.present')}
 health=[e for e in rows if e['stage']=='health'];routes=collections.Counter(e['stage'] for e in rows if e['stage'].startswith('route.'))
 failures=collections.Counter(e['stage'] for e in rows if e['stage'] in ('rtp.assembly_drop','viewer.queue_overflow','video.recovery_epoch','idr.receiver_request','rtp.sequence_gap','rtp.late_recovery','rtp.unresolved_expired'))
 reports.append(dict(file=name,window_start_us=start,window_end_us=end,seconds=duration,complete_window=duration>=a.seconds,invalid_lines=invalid,streams=streams,stalls=stalls,summary=summary,routes=routes,events=failures,dropped=max((e.get('dropped',0) for e in health),default=0),health=health))
pathlib.Path(a.out).write_text(json.dumps(reports,indent=2,ensure_ascii=False),encoding='utf-8')
for r in reports:print(json.dumps({k:r[k] for k in ('file','seconds','complete_window','streams','routes','events','dropped')},ensure_ascii=False))
