const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const elements={},clock={now:100},sent=[];

// element 创建最小 DOM 测试替身，保留监听器以检查失焦释放，不依赖浏览器或第三方包。
function element(id) {
  if(elements[id]) return elements[id];
  return elements[id]={width:1920,height:1080,checked:false,disabled:false,textContent:'',events:{},
    // addEventListener 记录真实页面绑定的事件处理函数，供测试直接触发。
    addEventListener(name,handler){this.events[name]=handler;},
    // getContext 提供画布初始化所需的最小上下文，本测试不模拟解码显示。
    getContext(){return {};},
    // getBoundingClientRect 返回可调整的 CSS 尺寸，测试缩放和内部留白映射。
    getBoundingClientRect(){return this.rect;},
    rect:{left:100,top:50,width:960,height:540}
  };
}

const document=element('document');
// querySelector 将页面查询映射到测试元素，复用相同对象保存状态。
document.querySelector=function(id){return element(id);};
// hasFocus 模拟前台窗口，后台测试通过 hidden 单独控制可见性。
document.hasFocus=function(){return true;};
const window=element('window');window.devicePixelRatio=2;
const scope=vm.createContext({document,window,console,WebSocket:{OPEN:1},sent,clock,
  performance:{
    // now 使用可控时钟验证移动限频，避免不稳定的真实定时等待。
    now(){return clock.now;}
  }
});
const script=fs.readFileSync(__dirname+'/index.html','utf8').match(/<script>([\s\S]*?)<\/script>/)[1];
vm.runInContext(script,scope);

// run 执行页面中的真实函数并取回 JSON 结果，消除 VM 对象原型差异。
function run(code){return JSON.parse(JSON.stringify(vm.runInContext(code,scope)));}

assert.deepEqual(run('mousePoint({clientX:580,clientY:320})'),{x:0.5,y:0.5});
assert.equal(run('mousePoint({clientX:50,clientY:320})'),null);
assert.deepEqual(run('mousePoint({clientX:5000,clientY:-1},true)'),{x:1,y:0});
elements['#screen'].rect={left:100,top:50,width:1000,height:1000};
assert.equal(run('mousePoint({clientX:600,clientY:100})'),null);
assert.deepEqual(run('mousePoint({clientX:600,clientY:550})'),{x:0.5,y:0.5});

// 测试用发送函数只记录 JSON，不连接网络或触发系统鼠标。
vm.runInContext(`mouseReady=true;decoder={};waiting=false;mouseToggle.checked=true;
ws={readyState:1,bufferedAmount:0,
// send 记录前端实际生成的消息，检查关键操作不会被移动节流吞掉。
send(text){sent.push(JSON.parse(text));}};
const event={clientX:600,clientY:550,button:0,detail:2,target:canvas,
// preventDefault 为测试事件提供浏览器默认行为接口。
preventDefault(){}};
mouseMove(event);mouseMove(event);mouseDown(event);mouseUp(event);`,scope);
const actions=[];
for(const item of sent) actions.push(item.action);
assert.deepEqual(actions,['move','down','up']);
assert.equal(sent[1].clicks,2);
assert.equal(sent[1].button,0);
vm.runInContext('ws.bufferedAmount=10000;clock.now=200;mouseMove(event);mouseDown(event);',scope);
assert.equal(sent.at(-1).action,'down');
let moves=0;
for(const item of sent) if(item.action==='move') moves++;
assert.equal(moves,1);
window.events.blur();
assert.equal(sent.at(-1).action,'release');
assert.equal(elements['#mouse'].checked,true);
const pausedCount=sent.length;
vm.runInContext('mouseDown(event);clock.now+=100;mouseMove(event);event.deltaY=10;mouseWheel(event);mouseUp(event);',scope);
assert.equal(sent.length,pausedCount);
assert.equal(run('held.size'),0);
// hasFocus 验证仅收到 focus 事件但窗口实际仍未获得焦点时不会恢复。
document.hasFocus=function(){return false;};
window.events.focus();assert.equal(run('mouseSuspended'),true);
// hasFocus 恢复真实前台状态供后续交互测试使用。
document.hasFocus=function(){return true;};
window.events.focus();
assert.equal(run('mouseSuspended'),false);
document.hidden=true;document.events.visibilitychange();
window.events.focus();assert.equal(run('mouseSuspended'),true);
document.hidden=false;document.events.visibilitychange();
assert.equal(run('mouseSuspended'),false);
document.events.keydown({key:'Escape'});
assert.equal(elements['#mouse'].checked,false);
window.events.focus();assert.equal(elements['#mouse'].checked,false);
elements['#mouse'].checked=true;window.events.pagehide();
assert.equal(elements['#mouse'].checked,false);
vm.runInContext('mouseToggle.checked=true;event.button=2;mouseDown(event);mouseUp(event);event.deltaMode=1;event.deltaY=4;mouseWheel(event);',scope);
assert.equal(sent.at(-3).button,1);
assert.equal(sent.at(-2).action,'up');
assert.equal(sent.at(-1).delta,64);
assert.equal(sent.at(-1).type,'mouse');
vm.runInContext(`relayDevice='mac-test';relaySession='0123456789abcdef0123456789abcdef';sendMouse({action:'move',x:0.2,y:0.8});`,scope);
// 缓冲积压只会跳过移动，关键操作仍必须带上远程设备和会话外层。
vm.runInContext(`sendMouse({action:'down',x:0.2,y:0.8,button:0,clicks:1});`,scope);
assert.equal(sent.at(-1).type,'control');
assert.equal(sent.at(-1).device,'mac-test');
assert.equal(sent.at(-1).session,'0123456789abcdef0123456789abcdef');
assert.equal(sent.at(-1).mouse.type,'mouse');
const beforeHeartbeat=sent.length;
vm.runInContext(`mouseReady=false;sendMouse({action:'move',x:0.2,y:0.8});sendMouse({action:'heartbeat'});`,scope);
assert.equal(sent.length,beforeHeartbeat+1);
assert.equal(sent.at(-1).mouse.action,'heartbeat');
vm.runInContext(`message({target:{},data:JSON.stringify({type:'relay-session',device:'old',session:'stale'})});`,scope);
assert.equal(run('relayDevice'),'mac-test');
console.log('通过：归一化坐标、缩放、留白、DPR、移动限频、关键操作、双击计数、右键、滚动和失焦释放逻辑（未投递原生事件）。');
console.log('通过：Relay 控制消息绑定设备与会话，未启用鼠标时仍发送观看会话保活。');

vm.runInContext(fs.readFileSync(__dirname+'/windows/nine-points.js','utf8'),scope);
// 诊断脚本的逐点日志在测试中静音，最后只汇报矩阵结果。
scope.console={log(){}};
vm.runInContext('mouseReady=true;ws.bufferedAmount=0;',scope);
let pointCases=0;
for(const zoom of [0.8,1,1.25]) {
  for(const dpr of [1,1.25,1.5,2]) {
    window.devicePixelRatio=dpr;
    for(const rect of [{width:960,height:540},{width:1000,height:1000},{width:1440,height:500}]) {
      elements['#screen'].rect={left:33,top:57,width:rect.width*zoom,height:rect.height*zoom};
      // 1280×1024 主屏映射到 1920×1080 编码画布，包含编码黑边与 CSS 黑边两层。
      vm.runInContext('installNinePointTest({width:1280,height:1024,videoWidth:1920,videoHeight:1080,left:284,top:0,contentWidth:1350,contentHeight:1080});',scope);
      for(let i=0;i<9;i++) {
        vm.runInContext(`window.ninePoint(${i})`,scope);
        const msg=sent.at(-1).mouse;
        const px=[0,640,1279][i%3],py=[0,512,1023][Math.floor(i/3)];
        assert.ok(Math.abs(msg.x-(284+(px+0.5)*1350/1280)/1920)<1e-12);
        assert.ok(Math.abs(msg.y-(py+0.5)/1024)<1e-12);
        assert.equal(sent.at(-1).session,'0123456789abcdef0123456789abcdef');
        pointCases++;
      }
    }
  }
}
console.log(`通过：${pointCases} 组 Windows 九点浏览器逻辑，覆盖 CSS 缩放、DPR、编码与元素留白；未验证真实 GUI。`);

// 采集暂停必须重置解码器，但保留同一WebSocket和会话；恢复后的PTS允许从零重新开始。
scope.decodedPTS=[];scope.decoderCloses=0;
vm.runInContext(`
// VideoDecoder 测试替身仅验证生命周期与时间戳路由，不声称执行了H264解码。
class TestDecoder {
 // constructor 为真实页面恢复路径建立独立解码状态。
 constructor(){this.decodeQueueSize=0;}
 // configure 保留页面所需方法，本用例只检查生命周期。
 configure(){}
 // decode 记录跨恢复前后的PTS，验证新进程PTS零不会与旧解码器混用。
 decode(chunk){decodedPTS.push(chunk.timestamp);}
 // close 记录旧解码器是否在采集暂停时被关闭。
 close(){decoderCloses++;}
}
VideoDecoder=TestDecoder;
// EncodedVideoChunk 保留传入的时间戳和类型，用于检查页面封包路由。
EncodedVideoChunk=function(value){return value;};
mouseReady=true;mouseToggle.checked=true;held.add(0);decoder=new TestDecoder();waiting=false;
const sameWS=ws,sameSession=relaySession;
message({target:ws,data:JSON.stringify({type:'capture-status',state:'paused',message:'DXGI access lost'})});
`,scope);
assert.equal(run('decoderCloses'),1);
assert.equal(run('decoder===null && waiting && config===null'),true);
assert.equal(run('mouseToggle.checked && !mouseReady && held.size===0'),true);
assert.equal(run('ws===sameWS && relaySession===sameSession'),true);
assert.match(elements['#capture-status'].textContent,/paused/);
vm.runInContext(`
message({target:ws,data:JSON.stringify({codec:'avc1.42c028',width:1920,height:1080,fps:20})});
const recoveredPacket=new ArrayBuffer(18);
new DataView(recoveredPacket).setUint8(0,1);
message({target:ws,data:recoveredPacket});
message({target:ws,data:JSON.stringify({type:'capture-status',state:'running',message:'new IDR'})});
message({target:ws,data:JSON.stringify({type:'mouse-status',enabled:true,message:'ready'})});
`,scope);
assert.deepEqual(run('decodedPTS'),[0]);
assert.equal(run('!waiting && decoder!==null && mouseReady && mouseToggle.checked'),true);
assert.equal(run('config.codec'),'avc1.42c028');
console.log('通过：采集暂停释放拖动并重置解码器；同连接/会话从PTS零恢复，恢复状态不覆盖H264配置（未执行真实解码）。');
