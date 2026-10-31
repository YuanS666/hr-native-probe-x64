# Windows MF 软件编码 Agent：待人工公网验收

已接入用户实测通过的 DXGI + D3D11 + 系统软件 H.264 编码，不使用 FFmpeg。独立探针曾在 Intel HD Graphics 630 / 1920×1080 输出200帧、10个IDR，网页解码与恢复通过；**新的实时 Agent 集成仍待Windows实机、公网视频和鼠标验收**。硬件MFT不参与Agent路径，本次不用继续测硬编。

## 下载部署

从 Actions → Build Windows Native Probe 的最新成功任务下载 **windows-agent-mf-x64**，不要下载独立探针产物。解压到一个固定目录，保持：

```text
screen-agent-windows-amd64.exe
screen_capture.exe
README.md
```

两个EXE必须在同一目录，不需要加入PATH。目标电脑不需要安装FFmpeg、Go、Python、VS或第三方编码软件；需要Windows 10 1703+ / 11 x64的系统媒体功能和正常显卡驱动。Windows N可能需要系统Media Feature Pack。普通用户的物理控制台会话、SDR主屏，无RDP、旋转屏或安全桌面支持。只启动Go Agent，不要单独双击辅助程序。

Go Agent默认 `-encoder mf`，每个观看会话启动同目录下的 `screen_capture.exe --stream --encoder software`，退出即回收。原生端不断生成完整AU，用大端长度+原17字节视频头传给Go；Go保持原PTS、补入完整编码帧到达时间、IDR前发送原JSON配置，再通过现有WebSocket/Relay转发。目标主屏不是16:9时，原生编码黑边与已有Windows鼠标映射共用相同偶数几何规则。协议、Relay与浏览器无需更新。

这是一套两文件部署，不是单EXE；没有解压运行时组件或复杂打包器。两个文件来自同一构建，避免混用旧探针（需要native-capture revision=5）。实时模式不写H264、HTML或CSV，不保留逐帧统计历史；原生进程日志仍打印CPU/内存与输出FPS/码率，Go日志与页面保留原统计。它们不能测量纯GPU编码耗时或端到端显示延迟。

## 一次公网验收

1. 停止正在占用同一Relay设备的Mac/旧Windows Agent，保留VPS配置和现有两个身份密钥。
2. 在解压目录打开普通PowerShell，运行：

```powershell
$secret = Read-Host '输入现有 Relay Agent 密钥' -AsSecureString
$env:RELAY_AGENT_KEY = [System.Net.NetworkCredential]::new('', $secret).Password
.\screen-agent-windows-amd64.exe -mode agent -relay wss://erp.beiaiying.com:8443/agent -device-id '现有设备ID' -mouse
```

`现有设备ID`必须替换成VPS已授权的实际ID。Agent密钥与浏览器观看密钥不同，不把密钥放到命令参数、代码或GitHub中。Agent首先显示已连接、等待浏览器，只有点击开始后才会启动编码器。

3. 手机关闭Wi-Fi，用浏览器访问 `https://erp.beiaiying.com:8443/`，输入同一设备ID和**观看密钥**。点击开始；Agent应打印 `native-capture revision=5` 和 `MF streaming`，浏览器应显示实时Windows桌面。移动窗口/滚动网页确认真实更新，检查帧率和解码错误，再勾选鼠标控制。
4. 在普通无破坏性的窗口测试移动、左/右键、双击、拖动、上下滚动。拖动中失焦/断开应释放按钮；重新点击开始应重新出现画面，首帧是新IDR。至少先观察5分钟，确认无持续延迟或队列增长。
5. Ctrl+C停止Agent；执行 `Remove-Item Env:RELAY_AGENT_KEY` 清理该终端环境变量。无需停止VPS Relay。

若想先本地测试，也可运行 `.\screen-agent-windows-amd64.exe -mouse`，同机Chrome打开 `http://127.0.0.1:8080` 并点击开始。无需再测独立探针。编译/逻辑测试通过不代表实时集成成功，请反馈真实画面、控制效果和异常日志。

## 构建方式（继续用云构建）

上传本次完整Agent源码包中的根目录Go文件、`go.mod`、`index.html`、`windows/`以及新版 `.github/workflows/build-native-probe.yml`；此前只有探针的仓库缺少Go源码，不能只覆盖probe.cpp。不要把源码包ZIP本身当作仓库内容。

workflow继续编译C++，同时设置Go 1.25、运行 `TestNative` 协议及子进程测试，然后构建Go Agent，输出新的 `windows-agent-mf-x64` 产物。工作流没有在云端采集桌面，也不携带Relay配置或认证密钥。

若已有Windows构建工具，可手动：

```powershell
cmake -S windows/native-probe -B build -A x64
cmake --build build --config Release
$env:CGO_ENABLED='0'
go build -o release/screen-agent-windows-amd64.exe .
Copy-Item build/Release/screen_native_probe.exe release/screen_capture.exe
```

## 限制与排错

- 缺少辅助程序：把本次产物中两个EXE放在一起；Agent仅按自身目录查找，不搜索系统PATH。
- 旧辅助程序或畸形输出：Go校验包长度、NAL/AUD、IDR及参数集、PTS和ready占位，拒绝后结束观看会话，不跳过安全检查。
- 慢浏览器：WebSocket写超时后断开；管道同步反压，不丢弃任意已编码依赖帧，不积压无限发送队列。
- 编码异常：结束本次连接并释放鼠标；Agent仍按已有1–30秒退避重连，等待新观看会话，不自动无限重启编码器。
- 已修正发送EOF的错误分类：网络配置/视频发送失败会明确保留来源，不再因Go主动终止子进程而误报“系统H264编码进程意外结束”。用户报告的退出码1本身不能证明原生编码失败；仍需查找前置`FAILED:`行。新分类修订有进程回归测试，实际断线现场原因尚待日志确认。
- 显示布局改变/锁屏/UAC/高权限窗口仍受系统限制，不提权、不绕过安全桌面；原生按键释放被系统拒绝时不能保证成功。
- Windows MF参数范围：偶数输出≤3840×2160，1–60fps，目标码率10000–20000000 bit/s；默认1080p20fps2Mbps。`-test-pattern`不支持MF。旧FFmpeg方式仍可显式选择 `-encoder libx264`，但不作为本次部署路径。
- Relay既有30分钟会话到期策略保持不变；不能把不足30分钟的公网连接当作连续1小时稳定性验收。
- 独立指针合成仍未实现；鼠标控制能力与光标是否出现在捕获图像中是两件事。

本次修改：`main.go`增加MF分派、`capture.go`改Windows默认、`native_video.go`及其测试负责管道与进程生命周期，原生探针新增仅软件编码的连续输出模式；更新云构建和本说明。macOS原生采集/输入、Relay转发及认证逻辑、WebSocket、原H264分帧器和Go依赖保持不变；生产浏览器失焦处理见下方修订。

## revision 5：采集恢复与鼠标失焦

真实日志中的 `0x887a0026` 表示桌面复制接口失效，随后 `0x80070005` 为重新创建时访问被拒绝。日志不能证明是 AnyDesk 引起。

新版本在当前普通桌面尝试重建 duplication：最多10次，间隔250、500、1000、随后2000ms，累计等待最多13.75秒。期间不发送旧画面；成功获取新桌面帧后请求IDR，保留现有视频协议和PTS顺序。安全桌面、D3D设备丢失、主屏尺寸或旋转改变仍结束本次会话，不提升权限。恢复失败不会无休止重建编码器。Windows实际恢复效果待人工验证。

浏览器失焦/隐藏现在释放全部按钮并暂停输入，保留勾选；页面重新可见且获得焦点后恢复。不会恢复拖动按下状态。Esc、停止、页面离开及连接断开仍取消勾选。页面显示暂停/恢复原因，便于区分焦点变化和实际断线。

公网浏览器HTML内嵌于Relay，所以需要同时更新两个地方：

1. Windows替换新版 `windows-agent-mf-x64` 中两个EXE，启动日志确认revision=5；沿用原设备ID、密钥及命令。
2. VPS替换 `linux-relay-amd64` 产物中的 `screen-relay`。保留现有环境配置、证书、密钥和服务参数。先确认 `systemctl cat screen-relay` 的实际ExecStart；若与项目默认路径一致，将新文件上传为 `/tmp/screen-relay.new` 后执行：

```sh
sudo install -m 755 /tmp/screen-relay.new /opt/screen-relay/screen-relay.new
sudo cp -p /opt/screen-relay/screen-relay /opt/screen-relay/screen-relay.previous
sudo mv /opt/screen-relay/screen-relay.new /opt/screen-relay/screen-relay
sudo systemctl restart screen-relay
sudo systemctl status screen-relay --no-pager
```

服务重启会断开当前连接，Agent按现有退避重连。浏览器强制刷新现有HTTPS地址，再开始视频和鼠标控制。若部署路径/服务名不同，按实际配置替换，不能覆盖ERP的其他服务。

最小复测：拖动中切换到地址栏或其他程序，确认远端按钮释放、勾选保留；回到视频再拖动/滚动。按Esc后重新聚焦，不得自动开启控制。采集恢复只需在此前相同使用场景观察；若再有ACCESS_LOST，记录后面的retry/restored/FAILED，不必重跑独立编码探针。不建议故意开启UAC或切换安全桌面来触发。

本次本机能验证Go构建、协议/子进程测试、浏览器事件逻辑及可移植H264测试；不能在macOS代替Windows验证DXGI重建。GitHub云构建负责Windows原生编译，真实Windows恢复仍待人工验收。
