# Windows x64 Agent：待人工端到端验收

**当前部署入口：[系统软件 MF Agent](windows/MF_AGENT.md)。Windows默认编码已改为MF，无需预装FFmpeg；以下保留此前FFmpeg适配阶段的审查与验证记录，不能替代本次实时集成验收。**

本次沿用原有项目、Relay 和浏览器，未添加 Go 依赖。开发机为 macOS arm64，没有 Windows GUI、Wine 或 PowerShell；沙箱无法监听本地端口或访问真实屏幕，浏览器启动亦受 WindowServer 限制。没有尝试扩大权限。Mac 与公网控制的历史成功来自用户确认，不计作本次 Windows 或 Mac 实机回归证据。

## 审查与技术决策

| 现有文件 | 职责及兼容结论 |
| --- | --- |
| main.go | 本地服务、FFmpeg 启停及编码输出；仅抽出系统采集参数、按系统选择默认值 |
| h264.go | 按 AUD 组装完整 Access Unit，兼容 3/4 字节起始码与跨 Read；保持不变 |
| websocket.go | 标准库 WebSocket、长度限制、掩码和读写超时；保持不变 |
| agent.go / relay.go | 主动 WSS、两种身份密钥、设备授权、随机会话隔离；仅 Agent 成功提示改为跨平台措辞 |
| index.html / mouse.go | Annex B WebCodecs、CSS 内容区坐标与鼠标消息校验；保持不变 |
| mouse_darwin.go | macOS 原生输入与权限；保持不变 |

Windows 使用 gdigrab 采集**主显示器物理矩形**，libx264 ultrafast/zerolatency，默认 1920×1080、20 FPS、2 Mbps、yuv420p、无 B 帧、GOP 20、AUD。目标码率不代表静态桌面必须达到 2 Mbps。保持等比例缩放并填黑边，内容区使用偶数尺寸和偶数偏移，输入端复用同一几何计算。支持多屏环境中的主屏；不提供副屏切换、拼接桌面或 RDP 支持。主屏原点为 (0,0)，SendInput 使用完整虚拟桌面范围，支持其负原点。

协议未变：JSON codec 配置，视频包 17 字节头（IDR:u8、PTS:u64、帧就绪 Unix 微秒:u64）加 Annex B AU；SPS/PPS 随 IDR，PTS 按固定帧率递增。浏览器不传 AVC description。Relay 外层仍为 16 字节会话 + opcode；鼠标仍为绑定 device/session 的 control JSON。每个新观看会话启动编码器产生新 IDR；重置解码器后等待 IDR，不把任意 stdout Read 当帧。

输入使用标准库调用 SendInput，x64 INPUT=40 字节、联合体偏移=8、MOUSEINPUT=32；静态断言和单元测试检查 ABI。进程设置 PerMonitorV2（旧 Windows 10 回退 PerMonitor），按物理像素映射，不重复乘 DPI 或浏览器 DPR。浏览器处理 CSS 留白，Windows 再扣除编码黑边；黑边点击忽略，拖动移动可夹到屏幕边缘，UP 不因落在黑边被吞掉。双击保持浏览器两组 down/up，滚轮按原协议换算。显示布局或 DPI 改变会终止控制，需重连。

只允许已登录物理控制台 WinSta0/Default 普通桌面。UIPI 可阻止操作高完整性窗口，返回错误码不能单独诊断 UIPI；不提权、不绕过 UAC、不操作安全桌面。锁屏、系统拒绝输入或进程被强杀时无法保证释放成功，明确记录失败；普通断开路径主动释放本会话按键。不要在远程按住鼠标时触发 UAC/锁屏。

## 修改文件

- `capture.go`、`capture_darwin.go`、`capture_windows_amd64.go`、`capture_unavailable.go`：平台默认值、采集参数和支持范围。
- `mouse_windows_amd64.go`、`windows_mouse.go`：原生 API、DPI/桌面检查、物理坐标与按键状态；`mouse_unavailable.go` 排除已支持的 Windows x64。
- `main.go`、`agent.go`：接入采集参数及跨平台提示，保留网络和业务流程。
- `windows_mouse_test.go`、`windows_codec_test.go`、`capture_darwin_test.go`、`mouse_test.cjs`、`relay_test.go`：ABI、九点、黑边、事件顺序、部分失败、固定流及异常退出测试。
- `windows/validate.ps1`、`windows/nine-points.js`、`windows/latency-pattern.html`、`windows/fixture-test.js`：人工环境、定位、性能与固定流验收工具。PowerShell 工具在本机无法执行，待 Windows 核验。
- `README.md`、本文件：入口与证据边界。没有改动 Relay 配置、密钥或生产浏览器。

## 构建与自动验证

从项目根目录执行，需要已有 FFmpeg/libx264；固定流测试缺 FFmpeg 会失败，不跳过。

```sh
GOCACHE="$PWD/.cache/go-build" go test -race -count=1 ./...
node mouse_test.cjs
node --check windows/fixture-test.js
GOCACHE="$PWD/.cache/go-build" go vet ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOCACHE="$PWD/.cache/go-build" go vet ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOCACHE="$PWD/.cache/go-build" go build -o .cache/screen-agent-windows-amd64.exe .
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOCACHE="$PWD/.cache/go-build" go test -c -o .cache/windows-tests.exe .
GOCACHE="$PWD/.cache/go-build" go build -o .cache/screen-mvp-mac .
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOCACHE="$PWD/.cache/go-build" go build -o .cache/screen-relay-linux .
```

当前实际证据：162 组后端纯逻辑九点、324 组前端 CSS/DPR/留白逻辑通过；前者模拟最大单轴误差 0px，**不是 SendInput 实测误差**。固定 libx264 流 40 AU，IDR=[0,20]，逐字节与正常读取结果一致，FFmpeg 软件解码通过。SHA256=`aba0a112d462d5900c6f39211d8ba23ff8050c025838760d33eeb1857c1a8181`（535278 字节，avc1.42c028）。这是 Mac 上真实 FFmpeg 的离线流，不是 Windows gdigrab。生成 `.cache/windows-validation/fixed-h264.html`，使用原页面解码函数验证正常40帧、重置恢复30帧、坏包恢复30帧（该项预期1个错误）。本机未运行实际 WebCodecs，需在 Chrome 打开此文件并点击测试，保存真实 PASS/FAIL；安全上下文不支持时保持未验证，不关闭浏览器防护。

现有测试使用 net.Pipe 内的真实 TLS/WebSocket 验证授权、错误设备/来源、旧会话重放、证书拒绝、单观看者、消息上限、慢客户端断开和鼠标释放。新增编码异常测试验证释放与一次调用后退出。它们没有监听本地端口，也不是公网端到端测试。

## Windows 实机运行

将 `.cache/windows-agent-release.zip` 解压到 Windows 本地固定目录。也可在项目根目录用 Windows Go 1.25+ 执行 `go build -o screen-agent-windows-amd64.exe .`。已有 FFmpeg 必须位于 PATH，且实际支持 gdigrab/libx264；本次未自动安装或覆盖配置。

普通用户 PowerShell，先记录环境（不同 DPI/显示布局分别保留输出）：

```powershell
.\windows\validate.ps1 -Mode info -AgentPath .\screen-agent-windows-amd64.exe
.\screen-agent-windows-amd64.exe -check-permissions
.\screen-agent-windows-amd64.exe -mouse -width 1920 -height 1080 -fps 20 -bitrate 2000000
```

本机 Chrome 访问 http://127.0.0.1:8080，先播放确认真实画面再勾选鼠标控制。停止本地进程后进行公网测试。若组织策略禁止执行 ps1，保留限制，手动运行脚本列出的查询命令；不要使用 ExecutionPolicy Bypass。

**现有 Relay 只允许一个 Agent。先停止 Mac Agent，使用 Relay 当前配置的设备 ID 和 Agent 密钥；不要另造 Windows 设备 ID。** `mac1` 以下仅为示例，必须替换为现有授权 ID。Agent 密钥与浏览器观看密钥不同。

```powershell
$secret = Read-Host '现有 Relay 的 Agent 密钥' -AsSecureString
$env:RELAY_AGENT_KEY = [System.Net.NetworkCredential]::new('', $secret).Password
.\screen-agent-windows-amd64.exe -mode agent -relay wss://erp.beiaiying.com:8443/agent -device-id mac1 -device desktop -encoder libx264 -mouse -width 1920 -height 1080 -fps 20 -bitrate 2000000
```

浏览器访问 https://erp.beiaiying.com:8443/ ，输入现有设备 ID 和**观看密钥**。保留 VPS 现有受信 TLS 和 WSS，不跳过证书校验，不需要更改 Relay 或开放 Windows 入站端口。测试后关闭 Agent 并执行 `Remove-Item Env:RELAY_AGENT_KEY`。用外网手机（关闭 Wi-Fi）验证实际跨网链路。

## 可重复验收

1. 保存 Windows 版本/x64、屏幕数量、主屏物理尺寸、DPI、Go/FFmpeg 版本及 gdigrab/libx264 查询输出。`-check-permissions` 成功仅代表运行条件查询，不代表输入成功。
2. Chrome 打开 `fixed-h264.html`，点击测试，保存 PASS/FAIL；随后连接真实 Windows，检查实时内容、FPS、码率、错误和解码队列。固定流成功不替代真实采集成功。
3. 在无破坏性的普通窗口测试移动、左/右键、双击、拖动、上下滚动。拖动中关闭浏览器/断网/页面失焦，确认按钮释放。先测试普通窗口，不使用管理员窗口或 UAC。
4. 九点：将 `windows/nine-points.js` 粘贴到控制浏览器开发者工具 Console。依据 Agent 日志主屏/内容区填写 `installNinePointTest({width:1920,height:1080,videoWidth:1920,videoHeight:1080,left:0,top:0,contentWidth:1920,contentHeight:1080})`，此例只适用于实际相同尺寸。Windows 另开 PowerShell 执行 `.\windows\validate.ps1 -Mode points -ScreenWidth 1920 -ScreenHeight 1080`。按提示在浏览器依次运行 `ninePoint(0)` 到 `ninePoint(8)`；每次在 Windows **键盘**按 Enter 读取 GetPhysicalCursorPos，期间不碰本地鼠标。保存 CSV，最大单轴误差须≤5px。100%/125%/150% DPI 各重启 Agent 重复，再改变浏览器缩放（80%/100%/125%）和窗口宽高比重复。有条件再测非16:9主屏，按实际日志重填内容区。
5. 断网后恢复：录屏记录断线时刻、重连日志及首个新画面/首次可用点击时刻；检查指数退避不超过每30秒一次的稳定重试。重新登录/开始，确认新 IDR 和输入可用。任务管理器结束 **FFmpeg 子进程**，确认错误、释放、会话结束；无浏览器新会话时不得反复启动 FFmpeg。
6. 无密钥/错误密钥/错误设备应无法进入；同设备第二浏览器不能接管；浏览器 Console 发送旧或错误 session 的 control 消息应被拒绝并断开（仅在本人测试会话操作），不应执行输入。保留日志，不粘贴真实密钥。
7. Mac 回归：停止 Windows Agent，恢复原 Mac 运行命令与设备 ID，经原 Relay 检查视频、移动、点击、拖动及断线释放。本次没有实机代跑。

## 性能记录和未解决项

另开 Windows PowerShell：`Get-Process screen-agent-windows-amd64` 获取 ID，运行 `.\windows\validate.ps1 -Mode perf -AgentProcessId 实际ID -DurationSeconds 3600`。记录 Agent 与 FFmpeg 子进程每秒 CPU（整机百分比）、WorkingSet、PrivateBytes；首个 CPU 样本留空。浏览器载入 nine-points.js 后运行 `recordWindowsBrowserStats(3600)`，完成后 Console 执行 `copy(JSON.stringify(window.windowsBrowserSamples))` 保存。保持页面前台。Agent 退出会使采样失败；仅进程活着不等于连续视频成功，必须同时检查视频帧数、队列及错误趋势。

画面延迟：Windows 主屏打开 `windows/latency-pattern.html`，用同一台至少120fps相机同时拍到源屏和远端屏。匹配同一编号首次呈现的相机帧号，至少100个样本，CSV列 `SourceFrame,RemoteFrame,CaptureFPS`。用真实相机采集帧率，不用慢动作播放帧率。运行 `.\windows\validate.ps1 -Mode latency -SamplesPath .\camera.csv`，按帧差计算实际 glass-to-glass P95，目标≤300ms，注明帧量化误差和滚动快门影响。未采样不能填性能达标。

| 指标 | 当前结果 |
| --- | --- |
| Windows FPS/实际码率、CPU/内存 | 未验证，需真实 Windows 日志和 CSV |
| 单帧 H.264 编码耗时 | 未验证；当前裸流没有采集时间，输出帧间隔不能替代编码耗时 |
| 实际 WebCodecs 解码队列/恢复 | 未验证，固定流页面与真实连接均需浏览器执行 |
| P95 端到端画面延迟≤300ms | 未验证，必须使用真实光学样本；RTT 不替代 |
| 重连耗时、30分钟播放、1小时稳定性 | 未验证；需真实时序与视频证据 |
| Mac 本次真实回归 | 未验证；历史公网成功由用户确认 |

按严重程度：①缺少 Windows 实机证据，不能判定适配完成。②**现有 Relay Cookie/观看会话30分钟强制到期**，登录到开始播放还有耗时，当前无法满足单会话连续30分钟或1小时公网播放要求；本次没有为通过测试弱化到期策略。可先做本地1小时稳定性测试，但不能算公网验收；后续需要明确授权的会话续期设计。③主屏/普通控制台范围、UIPI与锁屏释放限制如上，不支持RDP或安全桌面。实际编解码性能瓶颈尚无数据，不据此重构。

最终状态：**待人工端到端验收**。编译、静态检查和进程内自动测试通过不等于 Windows Agent 已完成。
