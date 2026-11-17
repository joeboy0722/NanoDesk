"""Pair metadata traces by SSRC/sequence/RTP timestamp, never absolute cross-host latency."""
import argparse,json,collections,datetime,pathlib,math,bisect
p=argparse.ArgumentParser();p.add_argument('host');p.add_argument('viewer');p.add_argument('--out',required=True);a=p.parse_args()
def read(path):
 for line in open(path,encoding='utf-8'):
  try:yield json.loads(line)
  except json.JSONDecodeError:continue
def q(v,p):return sorted(v)[max(0,math.ceil(len(v)*p)-1)] if v else None
def stats(v):return dict(n=len(v),p50=q(v,.5),p95=q(v,.95),max=max(v,default=None))
viewer=[];vp=[];first=None;last=None
for e in read(a.viewer):
 if e.get('utc'):
  t=datetime.datetime.fromisoformat(e['utc'].replace('Z','+00:00'));first=t if first is None else min(first,t);last=t if last is None else max(last,t)
 if e['stage']=='rtp.packet':vp.append((e.get('result',0),e.get('value',0),e.get('frame',0),e['mono_us']))
 elif e['stage'] not in ('summary','health') and not e['stage'].startswith('ui.'):viewer.append(e)
lo=(first-datetime.timedelta(seconds=5)).isoformat()[:19];hi=(last+datetime.timedelta(seconds=5)).isoformat()[:19]
host=[];hp=collections.defaultdict(list);hf=collections.defaultdict(dict);by_size=collections.defaultdict(list);dur=collections.defaultdict(list);counts=collections.Counter();ssrcs={x[0] for x in vp}
for e in read(a.host):
 st=e['stage']
 if not e.get('utc') or not lo<=e['utc'][:19]<=hi or st in ('summary','health') or st.startswith('ui.'):continue
 counts[st]+=1
 if 'duration_us' in e or st in ('capture.total','capture.acquire','encode.total','schedule.budget_actual','produce.interval','host.queue_age','host.write','relay.pace','relay.write','rtp.send_write','transport.write_sample','encoder.rebuild'):dur[st].append(e.get('duration_us',0)/1000)
 if st=='rtp.send':
  if e.get('result',0) in ssrcs:hp[(e.get('result',0),e.get('value',0),e.get('frame',0))].append(e['mono_us'])
  continue
 if st in ('relay.write','relay.pace','relay.pace_planned','rtp.send_write') and e.get('duration_us',0)<10000:continue
 host.append(e)
 if st in ('capture.total','encode.total','schedule.budget_planned','produce.frame'):hf[e.get('frame',0)][st]=e
for n,f in hf.items():
 if 'encode.total' in f and 'produce.frame' in f:by_size[f['encode.total'].get('value',0)].append(n)
samples=[e for e in viewer if e['stage']=='rtp.sample'];offsets=collections.Counter()
for v in samples[:300]:
 for difference in (0,6):
  for n in by_size[v.get('value',0)+difference]:offsets[(v['frame']-hf[n]['produce.frame']['value']*90)&0xffffffff]+=1
offset=offsets.most_common(1)[0][0];by_stamp={(f['produce.frame']['value']*90+offset)&0xffffffff:n for n,f in hf.items() if 'produce.frame' in f}
matched=sum(e['frame'] in by_stamp for e in samples)
routes=collections.Counter((e['stage'],e.get('object',0)) for e in host if e['stage'].startswith('route.'))
packet_gaps=[]
for previous,current in zip(vp,vp[1:]):
 if current[3]-previous[3]<=250000:continue
 before=hp.get(previous[:3],[]);after=hp.get(current[:3],[])
 packet_gaps.append(dict(viewer_start_us=previous[3],viewer_end_us=current[3],viewer_gap_ms=(current[3]-previous[3])/1000,previous_sequence=previous[1],sequence=current[1],timestamp=current[2],sender_match_counts=[len(before),len(after)],sender_completion_interval_ms=(after[0]-before[0])/1000 if len(before)==len(after)==1 else None))
host.sort(key=lambda e:e['mono_us']);ht=[e['mono_us'] for e in host]
write_by_source={e.get('frame',0):e for e in host if e['stage']=='transport.write_sample'}
sv=[e for e in viewer if e['stage']=='render.new_submit'];stalls=[]
for previous,current in zip(sv,sv[1:]):
 if current['mono_us']-previous['mono_us']<=500000:continue
 stamps=(previous.get('frame',0),current.get('frame',0));numbers=[by_stamp.get(s) for s in stamps]
 detail=dict(viewer_start_us=previous['mono_us'],viewer_end_us=current['mono_us'],viewer_gap_ms=(current['mono_us']-previous['mono_us'])/1000,host_frames=numbers)
 if all(n in hf and 'encode.total' in hf[n] for n in numbers):
  start=hf[numbers[0]]['encode.total']['mono_us'];end=hf[numbers[1]]['encode.total']['mono_us']
  current_source=hf[numbers[1]]['produce.frame']['value'];write=write_by_source.get(current_source)
  if write:end=max(end,write['mono_us'])
  ctx=host[bisect.bisect_left(ht,start-100000):bisect.bisect_right(ht,end+100000)]
  detail['host_context_start_us']=start;detail['host_context_end_us']=end
  detail['host_summary']={st:dict(count=sum(e['stage']==st for e in ctx),duration_ms=stats([e.get('duration_us',0)/1000 for e in ctx if e['stage']==st])) for st in ('capture.total','capture.acquire','encode.total','schedule.budget_actual','produce.interval','host.queue_age','host.write','transport.write_sample','rtp.send_write','relay.write')}
  detail['host_key_events']=[e for e in ctx if e['stage'].startswith('idr.') or e['stage']=='produce.frame' and e.get('result',0)==1]
  detail['host_bitrate_kbps']=sorted({e.get('result',0) for e in ctx if e['stage']=='schedule.budget_planned'})
  detail['host_max_encoded_bytes']=max((e.get('value',0) for e in ctx if e['stage']=='encode.total'),default=0)
  detail['host_events']=ctx
 detail['packet_gaps']=[g for g in packet_gaps if previous['mono_us']-100000<=g['viewer_end_us']<=current['mono_us']+100000]
 stalls.append(detail)
result=dict(clock_policy='SSRC/sequence/timestamp pairing; durations stay in each process; sender-completion intervals compared to receiver-arrival intervals are not one-way latency.',host_filter_utc=[lo,hi],timestamp_offset=offset,matched_samples=matched,total_viewer_samples=len(samples),viewer_ssrcs=list(ssrcs),host_routes=[dict(stage=k[0],object=k[1],count=v) for k,v in routes.items()],host_durations_ms={st:stats(v) for st,v in dur.items()},host_counts=counts,host_rebuilds=[e for e in host if e['stage'] in ('encoder.rebuild_requested','encoder.rebuild')],host_overflows=[e for e in host if e['stage']=='host.queue_overflow'],host_bitrate_frames=dict(collections.Counter(f['schedule.budget_planned'].get('result',0) for f in hf.values() if 'schedule.budget_planned' in f)),packet_gaps=packet_gaps,stalls=stalls)
pathlib.Path(a.out).write_text(json.dumps(result,indent=2,ensure_ascii=False),encoding='utf-8')
print(json.dumps({k:result[k] for k in ('matched_samples','total_viewer_samples','host_routes')},ensure_ascii=False))
for s in stalls:
 print(json.dumps({k:v for k,v in s.items() if k not in ('host_events','host_summary','host_key_events')},ensure_ascii=False))
