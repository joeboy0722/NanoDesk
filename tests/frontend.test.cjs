const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const { webcrypto } = require('node:crypto');

function fixture() {
  const elements = new Map(), sockets = [], decoders = [], requests = [], intervals = [], windowHandlers = {};
  let now = 1000;
  class Element {
    constructor(tagName='DIV') { this.tagName=tagName; this.style={};this.children=[];this.handlers={};this.value='';this.checked=true;this.clientWidth=1024;this.clientHeight=768;this.width=800;this.height=600;this.classes=new Set();
      this.classList={add:v=>this.classes.add(v),remove:v=>this.classes.delete(v),contains:v=>this.classes.has(v),toggle:v=>this.classes.has(v)?this.classes.delete(v):this.classes.add(v)};
    }
    addEventListener(type,fn){this.handlers[type]=fn;}
    append(...children){this.children.push(...children);}
    appendChild(child){this.children.push(child);}
    remove(){this.removed=true;}
    contains(el){return this.children.includes(el);}
    focus(){document.activeElement=this;}
    getContext(){return {drawImage(){},clearRect(){}};}
    getBoundingClientRect(){return {left:0,top:0,width:800,height:600};}
    set innerHTML(html){this.markup=html;this.children=[];}
    get innerHTML(){return this.markup || '';}
  }
  const document={getElementById(id){if(!elements.has(id))elements.set(id,new Element());return elements.get(id);},createElement(tag){return new Element(tag.toUpperCase());},querySelectorAll(){return [];},addEventListener(){},activeElement:null};
  document.getElementById('zoomSelect').value='fit';
  class Socket {
    static OPEN=1;
    constructor(url){this.url=url;this.readyState=1;this.sent=[];sockets.push(this);}
    send(data){this.sent.push(JSON.parse(data));}
    close(){this.closed=true;this.readyState=3;}
  }
  class Decoder {
    constructor(callbacks){this.callbacks=callbacks;this.state='unconfigured';this.decodeQueueSize=0;this.decoded=[];decoders.push(this);}
    configure(config){this.config=config;this.state='configured';}
    close(){this.state='closed';}
    decode(chunk){this.decoded.push(chunk);}
  }
  class XHR {
    constructor(){this.upload={};this.headers={};requests.push(this);}
    open(method,url){this.method=method;this.url=url;}
    setRequestHeader(k,v){this.headers[k]=v;}
    send(data){this.data=data;}
    abort(){if(this.onabort)this.onabort();if(this.onloadend)this.onloadend();}
  }
  const context={document,console:{log(){},warn(){},error(){}},WebSocket:Socket,VideoDecoder:Decoder,EncodedVideoChunk:class{constructor(values){Object.assign(this,values);}},MediaSource:class{},JMuxer:class{feed(){}destroy(){}},XMLHttpRequest:XHR,FormData:class{append(){}},performance:{now:()=>now},navigator:{onLine:true},localStorage:{removeItem(){}},crypto:webcrypto,Uint8Array,DataView,ArrayBuffer,setInterval(fn){intervals.push(fn);return intervals.length;},clearInterval(){},setTimeout(){return 1;},clearTimeout(){},requestAnimationFrame(fn){fn();},fetch(){return Promise.reject(new Error('unused'));}};
  context.window=context;context.location={protocol:'https:',host:'localhost:8080'};context.addEventListener=(type,fn)=>{(windowHandlers[type] ||= []).push(fn);};
  const source=fs.readFileSync(path.join(__dirname,'../web/assets/app.js'),'utf8').replace(/\}\)\(\);\s*$/,`globalThis.hooks={handleControlMessage,handleBinaryVideoData,renderLaserPointer,canAutoClipboard,uploadFiles,connectWebSocket,applyZoom,getCanvasCoordinates,initDecoder,setToken(value){currentToken=value;},getDecoder(){return decoder;}};})();`);
  vm.runInNewContext(source,context);
  return {hooks:context.hooks,elements,sockets,decoders,requests,intervals,windowHandlers,advance(ms){now+=ms;},dispatchWindow(type,event){for(const fn of windowHandlers[type] || []) fn(event);}};
}

function authorize(f){f.hooks.setToken('secret');f.hooks.connectWebSocket();f.sockets.at(-1).onopen();f.hooks.handleControlMessage({type:'auth_success',role:2,role_name:'standard'});f.hooks.handleControlMessage({type:'my_info',id:'me',name:'me',color:'#123456'});}

test('untrusted names are text in both collaboration surfaces',()=>{
  const f=fixture(),name='<svg/onload=alert()>';
  f.hooks.handleControlMessage({type:'members_update',members:[{id:'1',name,color:'#123456',role:2,role_name:'standard'}]});
  const row=f.elements.get('membersList').children[0];assert.equal(row.children[0].children[1].textContent,name);assert.equal(row.innerHTML,'');
  f.hooks.renderLaserPointer({id:'1',name,color:'#123456',x:0,y:0});
  const pointer=f.elements.get('laserContainer').children[0];assert.equal(pointer.children[1].textContent,name);assert.equal(pointer.innerHTML,'');
});
test('clipboard follows controller, switch and local override',()=>{
  const f=fixture();authorize(f);
  assert.equal(f.hooks.canAutoClipboard(),false);
  f.hooks.handleControlMessage({type:'control_state',is_free:false,controller_id:'me',local_active:false});assert.equal(f.hooks.canAutoClipboard(),true);
  f.elements.get('clipboardAuto').checked=false;assert.equal(f.hooks.canAutoClipboard(),false);
  f.elements.get('clipboardAuto').checked=true;
  f.hooks.handleControlMessage({type:'control_state',is_free:true,local_active:true});assert.equal(f.hooks.canAutoClipboard(),false);assert.equal(f.elements.get('controlStatusBadge').textContent,'本機操作中');
});
test('token travels as WS message and upload header, never URL',()=>{
  const f=fixture();authorize(f);assert.ok(!f.sockets[0].url.includes('secret'));assert.equal(f.sockets[0].sent[0].token,'secret');
  f.hooks.uploadFiles([{name:'file.txt',size:3}]);const xhr=f.requests[0];assert.equal(xhr.url,'/upload');assert.equal(xhr.headers['X-Auth-Token'],'secret');
  f.elements.get('cancelUploadBtn').handlers.click();assert.match(f.elements.get('uploadProgressDetail').textContent,/已取消/);
});
test('upload success requires complete server results',()=>{
  const f=fixture();authorize(f);f.hooks.uploadFiles([{name:'file.txt',size:3}]);const xhr=f.requests[0];xhr.status=200;xhr.responseText='{"status":"ok","results":[]}';xhr.onload();assert.match(f.elements.get('uploadProgressDetail').textContent,/傳輸失敗/);
});

test('upload follows host limit above 4 GiB and still rejects oversized batches',()=>{
  const f=fixture();authorize(f);
  const init=limit=>f.hooks.handleControlMessage({type:'init',monitors:[],current:0,max_upload_bytes:limit});
  const gib=1024**3;
  f.hooks.uploadFiles([{name:'large.bin',size:5*gib}]);assert.equal(f.requests.length,0);
  init(2*gib);
  const decoderCount=f.decoders.length;
  f.hooks.handleControlMessage({type:'upload_limits',max_upload_bytes:10*gib});
  assert.equal(f.decoders.length,decoderCount); // A setting change must not reset the video decoder.
  f.hooks.uploadFiles([{name:'too-large.bin',size:11*gib}]);assert.equal(f.requests.length,0);
  f.hooks.uploadFiles([{name:'large.bin',size:5*gib}]);assert.equal(f.requests.length,1);
});

function mouseEvent(button=0){return {button,clientX:120,clientY:80,preventDefault(){this.prevented=true;}};}
function dispatchDisplayMouse(f,el,type,event){
  if(el.handlers[type])el.handlers[type](event);
  f.dispatchWindow(type,event); // DOM mouse events bubble to window.
}

test('double click forwards exactly down/up/down/up on canvas and video',()=>{
  for(const id of ['screenCanvas','screenVideo']){
    const f=fixture();authorize(f);const el=f.elements.get(id);
    for(let i=0;i<2;i++){
      const down=mouseEvent();dispatchDisplayMouse(f,el,'mousedown',down);assert.equal(down.prevented,true);
      dispatchDisplayMouse(f,el,'mouseup',mouseEvent());
    }
    const buttons=f.sockets[0].sent.filter(m=>m.type==='mouse_button');
    assert.deepEqual(buttons.map(m=>m.isDown),[true,false,true,false]);
    assert.ok(buttons.every(m=>m.button===0));
  }
});

test('drag release outside display is sent once and unrelated mouseup is ignored',()=>{
  const f=fixture();authorize(f);const el=f.elements.get('screenCanvas');
  f.dispatchWindow('mouseup',mouseEvent());
  dispatchDisplayMouse(f,el,'mousedown',mouseEvent(2));
  f.dispatchWindow('mouseup',mouseEvent(2));f.dispatchWindow('mouseup',mouseEvent(2));
  assert.deepEqual(f.sockets[0].sent.filter(m=>m.type==='mouse_button').map(m=>[m.button,m.isDown]),[[2,true],[2,false]]);
  dispatchDisplayMouse(f,el,'mousedown',mouseEvent());f.dispatchWindow('blur',{});f.dispatchWindow('mouseup',mouseEvent());
  assert.equal(f.sockets[0].sent.at(-1).type,'release_all_keys');
});
test('failed decoder is replaced and codec is derived from SPS',()=>{
  const f=fixture();authorize(f);
  const packet=new Uint8Array([1,0,0,0,1,0,0,0,1,0x67,0x42,0xe0,0x28,0]);f.hooks.handleBinaryVideoData(packet.buffer);
  const decoder=f.hooks.getDecoder();assert.equal(decoder.config.codec,'avc1.42e028');assert.equal(decoder.decoded.length,1);
  decoder.state='closed';decoder.callbacks.error(new Error('decode failed'));assert.notEqual(f.hooks.getDecoder(),decoder);
  assert.ok(f.sockets[0].sent.some(m=>m.type==='request_keyframe'));
});
test('heartbeat cannot conceal a stalled video and revocation resets UI',()=>{
  const f=fixture();authorize(f);f.advance(4000);f.hooks.handleControlMessage({type:'ping'}); // Exercise actual incoming socket event to refresh heartbeat.
  f.sockets[0].onmessage({data:'{"type":"ping"}'});f.intervals.at(-1)();assert.match(f.elements.get('statusText').textContent,/畫面停滯/);
  f.sockets[0].onclose({code:1008});assert.equal(f.elements.get('authOverlay').classList.contains('hidden'),false);assert.equal(f.elements.get('statusText').textContent,'請重新授權');
});
