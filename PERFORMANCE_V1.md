# V1.0 使用体验与性能诊断

日期：2026-10-31。状态：源码审查和浏览器逻辑检查已执行；本轮没有真实Windows桌面、VPS访问凭据或双设备光学测量，A–E场景均待人工实测。未调整1920×1080、20 FPS、2 Mbps参数，未修改视频/输入协议、编码或重连策略。

## 当前证据与限制

| 项目 | 目前证据 | 可得结论 |
|---|---|---|
| 公网视频、鼠标、基础键盘 | 用户此前真实设备反馈 | 基本操作链路可用；不代表已完成本轮定量体验测试 |
| Windows吞吐 | 此前用户日志19.76 FPS / 2.04 Mbps | 单个历史窗口接近20 FPS/2 Mbps；不能作为A–E的场景均值 |
| Windows资源 | 历史日志working约128 MiB，CPU窗口约1.55–23.93% of machine | 只包含原生采集编码子进程，非Go+子进程之和，不能跨场景比较 |
| 提交到编码输出 | 旧10秒探针200帧，mean8.31/P95 15.44/max45.71ms | 软件MFT短样本，含排队；不是纯编码耗时、当前公网延迟或整段稳定性 |
| 稳定性 | 用户此前修复后20分钟无断开 | 该版本和条件下的人工记录；不扩展为所有场景或1小时测试 |
| 浏览器绘制/队列、RTT、光学延迟、点击反馈、恢复时间 | 尚无本轮真实样本 | 未验证，不能报告P50/P95或承诺300ms |
| macOS资源与各场景性能 | 无可比样本 | 未验证 |

## 源码路径

- macOS：`capture_darwin.go:captureInput` AVFoundation采集→scale/yuv420p→`main.go:encodeVideo` VideoToolbox实时编码，无B帧、GOP=fps、AUD输出；`h264.go:readFrames/splitNAL`按AUD划分Annex B访问单元，下一NAL/AUD到达才能确认边界，有前瞻等待，不能仅凭代码量化实际延迟。`main.go`的PTS由帧序号合成，不是原始屏幕采集时刻。
- Windows：`native_video.go:encodeMFVideo`显式选择software；`windows/native-probe/probe.cpp:Capture::next/nv12/sample` DXGI复制BGRA、GPU转换NV12、CPU readback→`Encoder::submit/pump`系统软件H264。每帧分配NV12/readback纹理和内存；是开销候选，尚无分阶段测时证明它是瓶颈。静态桌面会重复已有画面，所以输出20 FPS不等于每秒20次桌面变化。
- `native_video.go:forwardNativeVideo`用4字节长度读取完整包，检查AUD、SPS/PPS、IDR、PTS；17字节视频头为key/PTS/Go完整帧就绪时刻。`agent.go:sendAgentFrame`另加随机会话封装；`relay.go:serveAgent`校验会话并去除封装，原包转给浏览器。
- Agent→Relay→浏览器视频都是同步写入。`websocket.go:websocket.write/sendWSFrame`串行互斥，写超时3秒；Relay没有独立的应用层视频帧队列，不是“零缓冲”。内核TCP、TLS和浏览器接收任务仍可能缓冲；吞吐日志经过send后计数，受网络反压影响，不能当成纯编码速度。
- `index.html:message` WebCodecs Annex B解码；decodeQueueSize>4或pending>8则关闭解码器、清空并等待下一IDR；不会任意丢预测帧后继续解码。正常GOP约1秒，新浏览器启动新编码器；无浏览器即时请求IDR协议。阈值在下一包到达时检查，不是连续监控。
- `index.html:draw`立即Canvas绘制、finally关闭VideoFrame。绘制FPS是drawImage调用数，不是面板实际刷新或独立新画面数。decodeQueueSize不包含所有GPU、合成器或网络等待。
- 鼠标`mousePoint/mouseMove/sendMouse`处理留白/归一化，移动最多约30次/秒，发送积压>4096字节丢移动；按下/释放不走移动限频。消息经`serveViewer/sendCommand`、`agentConnection`及原生输入；没有输入执行ACK或画面因果关联，无法从现有时间戳算点击反馈。
- 恢复：`runAgent`网络1–30秒退避；浏览器`scheduleReconnect`有限重试及认证预检，恢复后输入不自动勾选。Windows采集错误通过capture-status暂停，六次有限重建；`video_watchdog.go`首帧15秒/后续完整帧8秒无输出终止进程。macOS编码异常退出观看连接。以上是代码策略，不是实测恢复耗时。

## 本次最小诊断变更

只修改index.html统计：耗时显示最近秩P50/P95/max；每个实际秒级窗口存入 `window.remoteDiagnostics`，最多3600条，重连保留，刷新页面清空。记录接收/绘制FPS、负载Mbps、解码耗时、解码队列、pending帧、浏览器发送缓冲、等待IDR、错误和丢弃计数、连接代次及后台状态；每窗口最多120条原始解码耗时，超出显式标记。记录不包含按键、画面、认证密钥。

浏览器F12 Console，每个场景开始前：

```js
window.remoteDiagnostics.length=0
```

场景结束后复制JSON到以平台和场景命名的文件，例如windows-B.json：

```js
copy(JSON.stringify(window.remoteDiagnostics,null,2))
```

Chrome的copy是开发者工具命令。页面来自Go嵌入HTML，公网模式只需用现有方式重建/替换Relay后刷新页面；无需更换Windows原生模块或Agent。这里不进行新部署或构建流程优化。

每组把原始decodeSamplesMS合并后排序，P50/P95用ceil(n×p)-1的最近秩索引；有truncated标记时注明样本截断。不要平均每秒P95。FPS、码率按windowSeconds加权平均，并报告窗口P50/P95；errors/dropped是当前连接累计值，重连会归零，须按connection分组取增量。decodeQueue、pending、browserSendBytes仅秒级瞬时值，可能漏掉短峰值；browserSendBytes是浏览器发往Relay的积压，绝不是收到的视频缓冲。

## 同设备同网络场景协议

Windows/Mac分别跑一轮。每组记录：硬件/CPU/逻辑线程数、OS版本、浏览器版本、屏幕及编码尺寸、显示器/DPI缩放、编码器、20 FPS/2 Mbps实际启动参数、Wi-Fi/有线/蜂窝、VPS区域及同一页面内容。不关闭现有保护、不改变参数来改善结果。

先暖机30秒，保留启动阶段但不计入稳定窗口。A静止桌面300秒；B同一长网页持续匀速滚动120秒；C拖同一窗口绕屏幕120秒；D同一测试文档点击和输入120秒（不输入隐私内容）；E只断控制端网络5秒再恢复，重复至少10次，每次等待稳定30秒。顺序相同；条件允许每组重复3次。后台/隐藏窗口样本标记无效，避免节流混入前台体验。当前活跃连接不因30分钟Cookie到期硬切；若断线重连时Cookie已过期，单独记录认证事件，不计作DXGI故障。

每组同时保存原Agent stderr日志、浏览器JSON、系统资源采样、Relay socket采样和摄像记录。没有样本的格子填“未验证”，不要填0。

### CPU、内存、带宽、队列

Windows PowerShell另开终端，采样Go及采集进程，先确认只有一对进程：

```powershell
Get-Process screen-agent-windows-amd64,screen_capture | Select-Object Id,ProcessName,SessionId
$logical=[int](Get-CimInstance Win32_ComputerSystem).NumberOfLogicalProcessors
$rows=for($i=0;$i -lt 300;$i++) {
  $at=(Get-Date).ToUniversalTime().ToString('o')
  Get-CimInstance Win32_PerfFormattedData_PerfProc_Process |
    Where-Object { $_.Name -like 'screen-agent-windows-amd64*' -or $_.Name -like 'screen_capture*' } |
    Select-Object @{n='at';e={$at}},Name,IDProcess,@{n='cpuMachinePercent';e={[double]$_.PercentProcessorTime/$logical}},WorkingSetPrivate,PrivateBytes
  Start-Sleep -Seconds 1
}
$rows | Export-Csv -NoTypeInformation windows-A-resources.csv
```

按实际PID避免同名进程误计，首次性能计数器样本可舍弃；CPU是整机归一百分比，Go和helper相加才是Agent总体。Windows任务管理器GPU引擎另录，不把软件编码进程Video Encode=0当异常。

Mac用活动监视器找到Go及其ffmpeg子进程PID，替换以下数字：

```sh
ps -axo pid,ppid,comm | rg 'screen|ffmpeg'
top -l 300 -s 1 -pid 12345 -pid 12346 -stats pid,command,cpu,mem > mac-A-resources.txt
```

top首窗口舍弃；Mac CPU100%约一逻辑核，除逻辑核数后再与Windows整机百分比比较（`sysctl -n hw.logicalcpu`）。不要把两个平台不同内存口径混作同一指标。按场景报告CPU P50/P95、内存起止/峰值/趋势，而非只看单个瞬间。

VPS另终端，以原Relay监听8443为例：

```sh
for i in $(seq 1 300); do date -u +%FT%TZ; ss -tinm '( sport = :8443 )'; sleep 1; done > relay-A-sockets.txt
```

逐条连接区分Agent和浏览器，保留Recv-Q/Send-Q及TCP信息，避免合并连接；没有ss权限/版本支持则标记未采到。应用帧队列数量不适用（同步转发），不能将内核字节队列换算为精确帧数。H264 Mbps不包含WS/TLS/IP开销，实际链路带宽可用系统网卡统计，但其他流量必须记录。

### RTT（单独报告，不代替画面延迟）

控制端和Agent端分别到VPS做60次ping（Windows `ping -n 60 erp.beiaiying.com`，Mac `ping -c 60 erp.beiaiying.com`），保留逐次值和丢包，离线计算P50/P95。ICMP被禁时写未测得，不能填0。`ss -ti`仅作对应TCP链路的内核平滑RTT参考。现有WS ping/pong仅保活，不带测时样本；不将两段RTT相加冒充端到端交互延迟。

### 画面与点击反馈：同一相机测量

复用windows/latency-pattern.html，两平台均可在被控主屏打开。源屏与控制端屏幕放在同一240fps相机视野：找到同一编号首次实际出现于源屏和远端屏的录像帧号，相差帧数/录像实际FPS×1000。连续至少100个编号，报告P50/P95及样本量，固定屏幕区域避开扫描差异。240fps每帧约4.17ms，考虑两次边缘判定和显示扫描误差；60fps测量精度较差，不能报告小数毫秒精度。此法不需要两台电脑时钟同步；该100ms计数器不是100ms测量精度，读取的是录像中同一编号的两个首次出现时刻。

点击反馈：被控端打开普通页面按钮，按钮点击立即切换大色块。相机同时拍摄控制端鼠标/按键与控制端远程画面，从物理按下到远程色块首次改变计时，至少50次，报告P50/P95。它包含输入设备、浏览器、网络、原生注入、页面响应、采集编码和显示，正是操作体验；不能用sendMouse到WebSocket.send耗时替代。没有同场拍摄设备时这两项均标记未验证。

### 断线恢复

E场景录像同时拍到网络恢复操作和控制端画面，记录三个独立时刻：本地网络恢复、首个新编号重新显示、手工重新启用控制后首个操作得到反馈。主要指标是网络恢复→新鲜画面，不是WSS open；同时记录总停顿时间、是否需登录/手工开始。至少10次取P50/P95。浏览器秒级JSON只能辅助定位连接代次/绘制恢复，有约一个统计窗口不确定性，不能用它报告毫秒级恢复精度；不使用跨机日志相减。

## 瓶颈候选与优先级（非实测排名）

| 候选 | 源码证据/实际影响 | 收益与复杂度 | 验证门槛 |
|---|---|---|---|
| 1. 弱网视频积压与IDR恢复 | 同步TCP反压、浏览器接收积压不可见；只在解码队列过阈后等下一IDR。可能导致滚动/拖动反馈滞后 | 若确认积压，可减少陈旧画面和恢复等待；中等复杂度，必须保持参考帧链 | 同场光学P95及恢复P95变好，正常网络FPS/画质/控制顺序不回退，30分钟无队列趋势增长 |
| 2. Mac Annex B分帧前瞻 | Scanner需后续NAL/AUD确认边界，可能引入固定等待 | 若测到一个帧周期量级等待，降低Mac基线延迟；中等，边界正确性风险 | 可控分片测试+真实Mac光学P50/P95改善，完整AU/SPS/PPS/重连测试全部通过 |
| 3. Windows每帧资源分配/软件编码readback | Capture::nv12/sample每帧分配、同步Map读回；已见整机CPU窗口波动 | 可能减少CPU/GPU同步和抖动；中等，需先分阶段测时，不能承诺具体百分比 | 同设备同场景CPU P95/延迟P95改善且FPS不降、内存不持续增长 |

20FPS本身每帧50ms，30Hz移动限频也可能增加约一个发送周期的等待；这是配置和实现的时间尺度，不是测得的P95。当前软件MFT历史短测P95小于一个帧周期，尚无证据说明“必须先硬编码”。

暂不需要：重做构建/单EXE、替换传输架构、恢复不稳定硬编路径、大范围GPU零复制、提高帧率/码率来掩盖体验问题、全套监控平台。暂无实测证据支持这些成本。

**唯一下一步任务：完成本报告A–E的双平台同条件基准采样，重点用同相机量化滚动/拖动P95和Relay Send-Q。** 当前缺的是可比较的真实数据，不能专业地断言上述候选谁是主要瓶颈。取得数据后仅针对排名第一的已证实问题动手优化；本轮不直接修改队列或编码。
