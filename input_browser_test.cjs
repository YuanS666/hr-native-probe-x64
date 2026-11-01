const fs=require('node:fs');
const vm=require('node:vm');
const assert=require('node:assert/strict');
const elements={},sent=[],opened=[],timeouts=new Map(),intervals=new Map();let sequence=0,now=100,auth=204;const expectedErrors=[];

// element 为页面提供带焦点和事件列表的最小DOM，测试不调用真实浏览器或输入设备。
function element(id){
  if(elements[id])return elements[id];
  return elements[id]={checked:false,disabled:false,textContent:'',width:1920,height:1080,events:{},
    // addEventListener 保存全部监听器，检查Esc与键盘监听同时存在。
    addEventListener(name,fn){(this.events[name]??=[]).push(fn);},
    // focus 模拟显式进入视频焦点。
    focus(){document.activeElement=this;},
    // getContext 只提供绘图接口；本测试不执行H264解码。
    getContext(){return {
      // drawImage 只接收绘制调用，不生成视频或伪造解码结果。
      drawImage(){}
    };},
    // getBoundingClientRect 给出基础视频尺寸，坐标测试仍由mouse_test独立覆盖。
    getBoundingClientRect(){return {left:0,top:0,width:1920,height:1080};}
  };
}
const document={hidden:false,activeElement:null,
  // querySelector 保留同一元素身份，便于检查控制门禁。
  querySelector(id){return element(id);},
  // hasFocus 模拟当前页面前台状态，隐藏另行测试。
  hasFocus(){return !this.hidden;},
  events:{},
  // addEventListener 记录文档监听器，允许多个keydown处理函数。
  addEventListener(name,fn){(this.events[name]??=[]).push(fn);}
};
const window={VideoDecoder:true,events:{},
  // addEventListener 记录窗口焦点事件。
  addEventListener(name,fn){(this.events[name]??=[]).push(fn);}
};
class FakeSocket {
  static OPEN=1;static CLOSED=3;
  // constructor 记录新连接数量，验证每次只创建一个WebSocket。
  constructor(url){this.url=url;this.readyState=1;this.bufferedAmount=0;opened.push(this);}
  // send 记录经过原始协议包装的输入JSON。
  send(text){sent.push(JSON.parse(text));}
  // close 模拟主动停止，事件回调的处理规则与页面一致。
  close(){this.readyState=3;if(this.onclose)this.onclose({target:this});}
}
class FakeDecoder {
  // constructor 为生命周期测试提供最小解码器状态。
  constructor(){this.state='configured';this.decodeQueueSize=0;}
  // close 记录资源关闭，验证断线后不继续使用旧解码器。
  close(){this.state='closed';}
}
const scope=vm.createContext({document,window,sent,console:{
    // error 收集预期网络错误，随后断言次数，避免把故障模拟当成测试失败日志。
    error(error){expectedErrors.push(error);},log:console.log
  },WebSocket:FakeSocket,AbortController,VideoDecoder:FakeDecoder,
  location:{protocol:'https:',host:'relay.example:8443'},performance:{now(){return now;}},
  // fetch 模拟服务端授权状态，不访问任何公网。
  fetch:async function(){return {status:auth};},
  // setTimeout 使用可观察定时器，测试实际退避值而不缩短生产代码。
  setTimeout(fn,delay){const id=++sequence;timeouts.set(id,{fn,delay});return id;},
  // clearTimeout 检查主动停止能撤销待执行重连。
  clearTimeout(id){timeouts.delete(id);},
  // setInterval 保存实际统计回调，供虚拟时钟验证指标和记录容量。
  setInterval(fn){const id=++sequence;intervals.set(id,fn);return id;},
  // clearInterval 移除旧连接的统计，避免重连后多次计数。
  clearInterval(id){intervals.delete(id);}
});
vm.runInContext(fs.readFileSync('index.html','utf8').match(/<script>([\s\S]*?)<\/script>/)[1],scope);

// run 执行实际页面函数，保留生产逻辑而不复制输入或重连算法。
function run(code){return vm.runInContext(code,scope);}

// event 构造明确的键盘事件并记录preventDefault，模拟物理键而非IME文本。
function event(code,repeat=false){return {code,repeat,isComposing:false,preventDefault(){this.prevented=true;}};}

// closeRemote 模拟当前服务器主动断开，让真实页面处理重连与按键清理。
function closeRemote(){const socket=opened.at(-1);socket.readyState=3;socket.onclose({target:socket});}

// tick 执行一个生产退避回调并等待异步认证结束，返回请求的真实延迟值。
async function tick(){const [id,value]=timeouts.entries().next().value??[];assert.ok(value,'缺少重连定时器');timeouts.delete(id);await value.fn();return value.delay;}

// main 依次验证键盘门禁和重连竞态，所有断言失败都会使CI失败。
async function main(){
 run("start();relayDevice='device';relaySession='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';mouseReady=true;keyboardReady=true;decoder=new VideoDecoder();waiting=false;keyboardToggle.checked=true;keyboardChanged();");
 assert.equal(document.activeElement,elements['#screen']);
 // 数字小键盘必须按独立code转发，不能用event.key或强制开启NumLock替代。
 const numpad=['Numpad0','Numpad1','Numpad2','Numpad3','Numpad4','Numpad5','Numpad6','Numpad7','Numpad8','Numpad9','NumpadDecimal','NumpadEnter','NumpadAdd','NumpadSubtract','NumpadMultiply','NumpadDivide','NumpadEqual'];
 for(const code of numpad){scope.keyboardDown(event(code));assert.equal(sent.at(-1).mouse.code,code);assert.equal(sent.at(-1).mouse.action,'down');scope.keyboardUp(event(code));assert.equal(sent.at(-1).mouse.action,'up');}
 scope.keyboardDown(event('NumLock'));const lockCount=sent.length;scope.keyboardDown(event('NumLock',true));assert.equal(sent.length,lockCount);scope.keyboardUp(event('NumLock'));assert.equal(run('keyHeld.size'),0);

 for(const code of ['ControlLeft','KeyA'])scope.keyboardDown(event(code));
 assert.equal(sent.at(-1).mouse.type,'keyboard');assert.equal(sent.at(-1).mouse.code,'KeyA');
 assert.equal(sent.at(-1).session,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
 run('mouseToggle.checked=false;mouseChanged();');assert.equal(run('keyboardToggle.checked'),true);assert.equal(run('keyHeld.has("ControlLeft")'),true);

 scope.keyboardDown(event('KeyA',true));assert.equal(sent.at(-1).mouse.repeat,true);
 document.activeElement={};const before=sent.length;scope.keyboardDown(event('KeyB'));assert.equal(sent.length,before);
 document.activeElement=elements['#screen'];scope.keyboardUp(event('KeyA'));
 window.events.blur[0]();assert.equal(run('keyHeld.size'),0);assert.ok(sent.some(x=>x.mouse?.type==='keyboard'&&x.mouse.action==='release'));
 window.events.focus[0]();assert.equal(run('keyHeld.size'),0);
 run('keyboardToggle.checked=true;decoder=new VideoDecoder();waiting=false;');
 scope.keyboardDown(event('MetaLeft'));scope.keyboardDown(event('KeyC'));scope.keyboardUp(event('MetaLeft'));
 assert.equal(run('keyHeld.size'),0);assert.equal(sent.at(-2).mouse.code,'KeyC');assert.equal(sent.at(-2).mouse.action,'up');

 for(const fn of document.events.keydown)fn({...event('Escape'),key:'Escape'});
 assert.equal(elements['#keyboard'].checked,false);assert.equal(elements['#mouse'].checked,false);
 run("keyboardReady=false;keyboardToggle.checked=true;");const denied=sent.length;scope.keyboardDown(event('KeyA'));assert.equal(sent.length,denied);
 run('start();keyboardToggle.checked=true;mouseToggle.checked=true;');
 closeRemote();assert.equal(elements['#keyboard'].checked,false);assert.equal(elements['#mouse'].checked,false);
 const first=opened.length;assert.equal(await tick(),1000);assert.equal(opened.length,first+1);
 closeRemote();assert.equal(timeouts.size,1);run('stop()');assert.equal(timeouts.size,0);
 auth=401;run('start()');closeRemote();const expired=opened.length;await tick();assert.equal(opened.length,expired);assert.equal(run('wanted'),false);assert.match(elements['#stats'].textContent,/授权已过期/);
 auth=204;
 // 延迟的认证响应不能复活用户已停止的视频。
 let resolveFetch;scope.fetch=()=>new Promise(resolve=>{resolveFetch=resolve;});
 run('start()');closeRemote();const pending=tick();run('stop()');const stopped=opened.length;resolveFetch({status:204});await pending;assert.equal(opened.length,stopped);
 // 模拟持续网络错误，最多六次退避后停止，不无限重试。
 scope.fetch=async()=>{throw Error('network unavailable');};
 run('start()');closeRemote();const attempts=[];
 while(timeouts.size)attempts.push(await tick());
 assert.deepEqual(attempts,[1000,2000,4000,8000,16000,30000]);assert.equal(expectedErrors.length,6);assert.equal(run('wanted'),false);
 scope.fetch=async()=>({status:204});run('start()');document.hidden=true;closeRemote();assert.equal(timeouts.size,0);
 document.hidden=false;scope.mouseVisibility();assert.equal(timeouts.size,1);run('stop()');
 // 重连定时器启动后才转入后台，同样不能建立新连接或耗掉一次实际尝试。
 run('start()');closeRemote();const background=opened.length;document.hidden=true;await tick();assert.equal(opened.length,background);assert.equal(timeouts.size,0);
 document.hidden=false;scope.mouseVisibility();assert.equal(await tick(),1000);run('stop()');
 // 模拟连接半断开：认证预检十秒超时后继续有限退避，而非永久卡住。
 scope.fetch=async function(_url,options){return new Promise((_resolve,reject)=>{
   // abort 将生产超时信号转为预期的网络失败。
   options.signal.addEventListener('abort',function(){reject(Error('request timeout'));});
 });};
 run('start()');closeRemote();const hanging=tick();assert.equal(await tick(),10000);await hanging;assert.equal(timeouts.size,1);run('stop()');
 // 检查实际统计回调的分位数、真实窗口换算、截断标识与3600窗口记录上限；不伪造真实性能结论。
 run('start();received=40;rendered=38;bytes=500000;decodeMS=[1,2,3,100];');
 now+=2000;intervals.get(run('timer'))();
 let sample=run('window.remoteDiagnostics.at(-1)');
 assert.equal(sample.receivedFPS,20);assert.equal(sample.drawFPS,19);assert.equal(sample.videoMbps,2);
 assert.equal(sample.decodeToCanvasMS.p50,2);assert.equal(sample.decodeToCanvasMS.p95,100);
 assert.deepEqual(Array.from(sample.decodeSamplesMS),[1,2,3,100]);
 assert.equal(run('percentiles([])'),null);assert.equal(run('received'),0);
 run('decodeMS=Array(121).fill(1)');now+=1000;intervals.get(run('timer'))();
 sample=run('window.remoteDiagnostics.at(-1)');assert.equal(sample.decodeSamplesMS.length,120);assert.equal(sample.decodeSamplesTruncated,true);
 for(let i=0;i<3601;i++){now+=1000;intervals.get(run('timer'))();}
 assert.equal(run('window.remoteDiagnostics.length'),3600);run('stop()');assert.equal(intervals.size,0);
 console.log('通过：诊断P50/P95、FPS/码率窗口换算、原始样本截断和3600条记录上限（合成计数测试，不是性能实测）。');
 console.log('通过：键盘焦点/权限/重复/失焦清理、同会话包装、Esc停用；有限重连、过期认证、后台暂停及停止竞态。未验证原生输入和真实网络。');
}
// catch 将异步测试失败转为非零退出码，不能吞掉CI错误。
main().catch(error=>{console.error(error);process.exitCode=1;});
