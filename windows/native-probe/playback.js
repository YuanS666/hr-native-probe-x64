// nativePacket 还原探针保存的原协议包；离线 ready=0 不用于计算延迟。
function nativePacket(text) {
  const raw=atob(text), data=new Uint8Array(raw.length);
  for(let i=0;i<raw.length;i++) data[i]=raw.charCodeAt(i);
  return data.buffer;
}

// nativePause 按实际 PTS 间隔回放，定时器只控制离线节奏，不测量端到端视频延迟。
function nativePause(ms) {
  // resolvePause 在指定时长后恢复回放，不创建额外网络连接。
  return new Promise(function resolvePause(resolve){setTimeout(resolve,ms);});
}

// nativePlay 将真实 MF 输出交给原页面 message/VideoDecoder，验证完整播放或中途重置后的 IDR 恢复。
async function nativePlay(recover) {
  resetDecoder();config=null;received=0;rendered=0;errors=0;dropped=0;
  decodeMS=[];latencyMS=[];relaySession='native-offline-test';
  ws={readyState:WebSocket.OPEN,
    // close 只关闭离线输入对象，不连接 Agent 或 Relay。
    close(){this.readyState=WebSocket.CLOSED;}
  };
  message({target:ws,data:JSON.stringify(nativeInfo)});
  let expected=0, waitingForKey=true, previousPTS=null, resetAt=-1;
  for(let i=0;i<nativeFrames.length;i++) {
    const packet=nativePacket(nativeFrames[i]), view=new DataView(packet);
    const pts=Number(view.getBigUint64(1)),key=view.getUint8(0)===1;
    if(previousPTS!==null) await nativePause(Math.max(0,(pts-previousPTS)/1000));
    previousPTS=pts;
    // 重置放在第一帧后，要求后续真实 IDR 能独立恢复；不补造关键帧。
    if(recover && i===Math.max(1,Math.min(10,Math.floor(nativeFrames.length/3)))) {
      if(decoder) await decoder.flush();
      resetDecoder();waitingForKey=true;resetAt=i;
    }
    if(waitingForKey && key) waitingForKey=false;
    if(!waitingForKey) expected++;
    message({target:ws,data:packet});
    if(pending.size>8 || (decoder?.decodeQueueSize??0)>4) throw Error('实际解码积压，不能算作通过');
    stats.textContent=`真实 MF 离线回放 ${recover?'恢复测试':'完整测试'}：${i+1}/${nativeFrames.length}，已绘制 ${rendered}，错误 ${errors}`;
  }
  if(decoder) await decoder.flush();
  if(waitingForKey || errors || rendered!==expected || canvas.width!==nativeInfo.width || canvas.height!==nativeInfo.height) {
    throw Error(JSON.stringify({recover,expected,rendered,errors,waitingForKey,width:canvas.width,height:canvas.height}));
  }
  if(recover && resetAt>=0 && !nativeFrames.slice(resetAt).some(
    // isRecoveryKey 检查重置点后实际文件是否存在 IDR，避免未恢复也误报成功。
    function isRecoveryKey(frame){return new DataView(nativePacket(frame)).getUint8(0)===1;}
  )) throw Error('流太短，缺少重置后的 IDR');
  return {recover,expected,rendered,errors,dropped};
}

// nativeRun 串行验证完整解码与 IDR 恢复并显示明确结果，不把文件回放描述为公网或性能验收。
async function nativeRun() {
  const button=document.querySelector('#start');button.disabled=true;
  try {
    if(!window.isSecureContext||!window.VideoDecoder) throw Error('缺少安全上下文或 VideoDecoder；不要关闭浏览器安全防护');
    if(nativeFrames.length<3) throw Error('不足3帧，无法执行中途重置恢复测试');
    const results=[await nativePlay(false),await nativePlay(true)];
    stats.textContent='PASS：真实 MF 输出经现有 WebCodecs 函数解码及 IDR 恢复\n'+JSON.stringify(results,null,2)+'\n离线回放不代表实时屏幕传输或端到端延迟通过。';
    document.title='PASS - Native MF H264';
  } catch(error) {showError(error);document.title='FAIL - Native MF H264';}
  finally {resetDecoder();ws=null;button.disabled=false;}
}

document.querySelector('#start').textContent='验证真实 MF 视频（两次回放）';
document.querySelector('#start').onclick=nativeRun;
document.querySelector('#stop').disabled=true;
document.querySelector('#mouse-status').textContent='独立原生探针文件；不连接网络、不控制鼠标。';
stats.textContent=`原生输出 ${nativeFrames.length} 帧，${nativeInfo.codec}，${nativeInfo.width}×${nativeInfo.height}。点击按钮实际解码。`;
