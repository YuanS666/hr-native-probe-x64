// fixedDelay 给浏览器解码线程留出一帧周期，不以同步突发输入制造非生产性的队列过载。
function fixedDelay(resolve) {setTimeout(resolve,50);}

// fixedPacket 将固定流中的 Base64 协议包还原成 ArrayBuffer，不改变 17 字节视频头和 Annex B 内容。
function fixedPacket(text) {
  const raw=atob(text),bytes=new Uint8Array(raw.length);
  for(let i=0;i<raw.length;i++) bytes[i]=raw.charCodeAt(i);
  return bytes.buffer;
}

// fixedPlay 使用原页面 message/VideoDecoder 路径测试正常、重置恢复或坏包恢复，返回实际解码数量。
async function fixedPlay(mode) {
  resetDecoder();config=null;received=0;rendered=0;errors=0;dropped=0;
  relaySession='offline-fixture';
  ws={readyState:WebSocket.OPEN,
    // close 结束离线输入对象，不发起网络连接。
    close(){this.readyState=WebSocket.CLOSED;}
  };
  const initial={codec:fixedStream.codec,width:fixedStream.width,height:fixedStream.height,fps:fixedStream.fps};
  message({target:ws,data:JSON.stringify(initial)});
  for(let i=0;i<fixedStream.packets.length;i++) {
    if(i===10 && mode!=='normal') {
      await decoder.flush();
      if(rendered!==10) throw Error('恢复前未解码出前 10 帧');
      if(mode==='reset') resetDecoder();
      if(mode==='corrupt') message({target:ws,data:new ArrayBuffer(3)});
    }
    message({target:ws,data:fixedPacket(fixedStream.packets[i])});
    await new Promise(fixedDelay);
  }
  if(!decoder) throw Error('没有可用解码器');
  await decoder.flush();
  const expectedFrames=mode==='normal'?40:30;
  const expectedErrors=mode==='corrupt'?1:0;
  if(rendered!==expectedFrames || errors!==expectedErrors || canvas.width!==1920 || canvas.height!==1080) {
    throw Error(JSON.stringify({mode,rendered,expectedFrames,errors,expectedErrors,width:canvas.width,height:canvas.height}));
  }
  return {mode,decoded:rendered,expectedFrames,errors,expectedErrors,dropped};
}

// fixedRun 运行三组实际 WebCodecs 测试，明确输出 PASS/FAIL；不将离线流结果描述为 Windows 屏幕或网络验收。
async function fixedRun() {
  const button=document.querySelector('#start');button.disabled=true;
  try {
    if(!window.isSecureContext || !window.VideoDecoder) throw Error('缺少安全上下文或 VideoDecoder；保持未验证，不修改浏览器安全设置');
    const results=[];
    for(const mode of ['normal','reset','corrupt']) {
      stats.textContent='正在测试 '+mode;
      results.push(await fixedPlay(mode));
    }
    stats.textContent='PASS：固定流实际 WebCodecs 解码与恢复\n'+JSON.stringify(results,null,2)+'\n这不是 Windows gdigrab 或公网端到端测试，不报告性能延迟。';
    document.title='PASS - 固定 H.264 流验证';
  } catch(error) {
    stats.textContent='FAIL / 未验证：'+String(error);document.title='FAIL - 固定 H.264 流验证';console.error(error);
  } finally {resetDecoder();ws=null;button.disabled=false;}
}

document.querySelector('#start').textContent='验证固定 H.264 流';
document.querySelector('#start').onclick=fixedRun;
document.querySelector('#stop').disabled=true;
document.querySelector('#mouse-status').textContent='离线固定测试流；不连接 Relay，不采集屏幕，不控制鼠标。';
