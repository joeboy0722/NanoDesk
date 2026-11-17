"""Summarize metadata-only NanoDesk JSONL. Never combines clocks across processes."""
import argparse, collections, json, pathlib, glob
p=argparse.ArgumentParser();p.add_argument('files',nargs='+');p.add_argument('--out',required=True);a=p.parse_args()
report={'files':[]}
for filename in [f for pattern in a.files for f in (glob.glob(pattern) or [pattern])]:
    events=[];invalid=0
    with open(filename,encoding='utf-8') as f:
        for line in f:
            try: events.append(json.loads(line))
            except json.JSONDecodeError: invalid+=1
    events.sort(key=lambda e:e.get('mono_us',0))
    # Recompute gap recovery using extended sequence numbers. Historical online
    # counters used uint16 keys and can misidentify a new epoch as a late packet.
    streams={};active_ssrc={};gap_packets=0;recovered_packets=0;expired_packets=0;reset_unresolved=0;recovery_max_us=0
    for e in events:
        if e['stage']!='rtp.packet':continue
        obj=e.get('object',0);ssrc=e.get('result',0);key=(obj,ssrc);seq=e.get('value',0);now=e['mono_us']
        previous_ssrc=active_ssrc.get(obj)
        if previous_ssrc is not None and previous_ssrc!=ssrc:
            old=streams.pop((obj,previous_ssrc),None)
            if old:reset_unresolved+=len(old['missing'])
        active_ssrc[obj]=ssrc
        if key not in streams:
            streams[key]=dict(high=seq,cleaned=seq,missing={});continue
        st=streams[key];delta=(seq-st['high']%65536+32768)%65536-32768;extended=st['high']+delta
        if delta>0:
            for n in range(st['high']+1,extended):st['missing'][n]=now;gap_packets+=1
            st['high']=extended
        if extended in st['missing']:
            recovery_max_us=max(recovery_max_us,now-st['missing'].pop(extended));recovered_packets+=1
        if st['high']-st['cleaned']>=128:
            expired=[n for n in st['missing'] if st['high']-n>32767]
            for n in expired:st['missing'].pop(n);expired_packets+=1
            st['cleaned']=st['high']
    rtp_quality=dict(recomputed_from_raw_packets=True,observed_gap_packets=gap_packets,recovered_within_window=recovered_packets,unresolved_expired=expired_packets,unresolved_at_end=sum(len(st['missing']) for st in streams.values()),unresolved_on_ssrc_reset=reset_unresolved,max_recovery_us=recovery_max_us)
    # Viewer link is emitted at consumption, including in the headless live test.
    # Derive delivery intervals without treating RTP packet gaps as display FPS.
    last_delivery={};derived=[];decoder_links=collections.defaultdict(set)
    decoded_at={};last_presented={};present_attempts=0;present_unknown=0
    for e in events:
        if e['stage']=='viewer.decoder_link':
            obj=e.get('object',0);decoder_links[obj].add(e.get('value',0))
            if obj in last_delivery:
                derived.append(dict(stage='viewer.delivery_interval',object=obj,frame=e.get('frame',0),mono_us=e['mono_us'],duration_us=e['mono_us']-last_delivery[obj],derived=True))
            last_delivery[obj]=e['mono_us']
        if e['stage']=='decode.output' and e.get('frame',0):
            decoded_at[(e.get('object',0),e['frame'])]=e['mono_us']
        if e['stage']=='render.present':
            present_attempts+=1;obj=e.get('object',0);frame=e.get('frame',0)
            if not frame:present_unknown+=1;continue
            decoded=decoded_at.get((obj,frame))
            if decoded is not None:
                derived.append(dict(stage='render.age_since_decode',object=obj,frame=frame,mono_us=e['mono_us'],duration_us=max(0,e['mono_us']-decoded),derived=True))
            previous=last_presented.get(obj)
            if previous is None or previous[0]!=frame:
                if previous is not None:
                    derived.append(dict(stage='render.new_frame_interval',object=obj,frame=frame,mono_us=e['mono_us'],duration_us=e['mono_us']-previous[1],derived=True))
                last_presented[obj]=(frame,e['mono_us'])
    end_us=max((e.get('mono_us',0) for e in events),default=0)
    for obj,(frame,shown_at) in last_presented.items():
        derived.append(dict(stage='render.tail_age',object=obj,frame=frame,mono_us=end_us,duration_us=end_us-shown_at,derived=True))
    events.extend(derived);events.sort(key=lambda e:e.get('mono_us',0))
    groups=collections.defaultdict(list)
    for e in events:
        if e['stage'] not in ('summary','health'):groups[(e['stage'],e.get('object',0))].append(e)
    metrics=[]
    for (stage,obj),rows in groups.items():
        values=sorted(r.get('duration_us',0) for r in rows)
        q=lambda n:values[(len(values)*n+99)//100-1]
        metrics.append(dict(stage=stage,object=obj,count=len(rows),p50_us=q(50),p95_us=q(95),p99_us=q(99),max_us=values[-1]))
    stalls=[]
    for e in events:
        if e['stage'] not in ('produce.interval','render.interval','viewer.delivery_interval','render.new_frame_interval','render.tail_age') or e.get('duration_us',0)<=500000:continue
        end=e['mono_us'];start=end-e['duration_us'];obj=e.get('object',0)
        context_start=max(0,start-1000000)
        context=[r for r in events if context_start<=r.get('mono_us',0)<=end and r['stage'] not in ('summary','health','rtp.packet','rtp.send')]
        # Union of top-level measured spans; nested conversion/MF spans are context only.
        allowed=('schedule.budget_actual','capture.total','encode.total','encoder.rebuild') if e['stage']=='produce.interval' else ('ui.poll','render.paint','render.present','viewer.decode_total','decode.feed','viewer.queue_age','idr.receiver_wait')
        spans=[]
        for r in context:
            if r['stage'] in allowed and (e['stage']=='produce.interval' or r.get('object',0) in ({0,obj}|decoder_links[obj]|{peer for peer,decoders in decoder_links.items() if obj in decoders})):
                spans.append((max(start,r['mono_us']-r.get('duration_us',0)),min(end,r['mono_us'])))
        covered=0;last=start
        for lo,hi in sorted(spans):
            covered+=max(0,hi-max(lo,last));last=max(last,hi)
        stalls.append(dict(stage=e['stage'],object=obj,frame=e.get('frame'),context_start_us=context_start,start_us=start,end_us=end,duration_us=e['duration_us'],unknown_us=max(0,e['duration_us']-covered),events=context))
    health=[e for e in events if e['stage']=='health']
    report['files'].append(dict(file=str(pathlib.Path(filename).name),invalid_lines=invalid,dropped=max((e.get('dropped',0) for e in health),default=0),metrics=metrics,stalls=stalls,health=health,rtp_quality=rtp_quality,render_quality=dict(present_attempts=present_attempts,unknown_frame_attempts=present_unknown,objects_with_known_frames=len(last_presented))))
pathlib.Path(a.out).write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps({'files':len(report['files']),'stalls':sum(len(f['stalls']) for f in report['files']),'output':a.out}))
