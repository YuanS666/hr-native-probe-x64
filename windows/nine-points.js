// installNinePointTest 依据 Windows 日志中的真实屏幕和视频内容区安装九点定位工具，配置值必须来自被控机器。
function installNinePointTest(config) {
  const {width,height,videoWidth,videoHeight,left,top,contentWidth,contentHeight}=config;
  for(const value of [width,height,videoWidth,videoHeight,contentWidth,contentHeight]) {
    if(!Number.isFinite(value)||value<2) throw Error('必须填写实际物理屏幕和视频尺寸');
  }
  if(!Number.isFinite(left)||!Number.isFinite(top)||left<0||top<0||left+contentWidth>videoWidth||top+contentHeight>videoHeight) throw Error('视频内容区无效');
  // ninePoint 通过页面现有的 CSS 映射和已认证连接发送一次移动，测试不注入点击或改变浏览器安全策略。
  window.ninePoint=function(index) {
    if(!Number.isInteger(index)||index<0||index>8) throw Error('索引为 0–8，按从左到右、从上到下顺序');
    if(!mouseReady||!ws||ws.readyState!==WebSocket.OPEN) throw Error('先连接 Windows Agent，并确认原生鼠标状态可用');
    if(canvas.width!==videoWidth||canvas.height!==videoHeight) throw Error('当前视频分辨率与测试配置不一致');
    const px=[0,Math.floor(width/2),width-1][index%3];
    const py=[0,Math.floor(height/2),height-1][Math.floor(index/3)];
    const nx=(left+(px+0.5)*contentWidth/width)/videoWidth;
    const ny=(top+(py+0.5)*contentHeight/height)/videoHeight;
    const rect=canvas.getBoundingClientRect();
    const scale=Math.min(rect.width/videoWidth,rect.height/videoHeight);
    const clientX=rect.left+(rect.width-videoWidth*scale)/2+nx*videoWidth*scale;
    const clientY=rect.top+(rect.height-videoHeight*scale)/2+ny*videoHeight*scale;
    const point=mousePoint({clientX,clientY});
    if(!point) throw Error('目标未映射到画面内容区');
    sendMouse({action:'move',...point});
    console.log({index,expectedPhysicalX:px,expectedPhysicalY:py,normalized:point});
  };
  console.log('九点工具已安装。依次运行 ninePoint(0) 至 ninePoint(8)，每次在 Windows 本机按 Enter 记录实际物理坐标。');
}

// recordWindowsBrowserStats 在可见控制页面最多记录 3600 条统计文本，避免诊断工具自身无限积压数据。
function recordWindowsBrowserStats(seconds=3600) {
  if(!Number.isInteger(seconds)||seconds<1||seconds>3600) throw Error('时长为 1–3600 秒');
  const records=[];
  let timer;
  const began=performance.now();
  // sample 采集当前页面实际统计；不会用 RTT 或未同步时钟计算视频端到端延迟。
  function sample() {
    records.push({time:new Date().toISOString(),elapsedSeconds:(performance.now()-began)/1000,text:document.querySelector('#stats').textContent});
    if(records.length>=seconds) {clearInterval(timer);console.log('统计采集结束',records);}
  }
  timer=setInterval(sample,1000);
  window.windowsBrowserSamples=records;
  return timer;
}
