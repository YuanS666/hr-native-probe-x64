# macOS H.264 屏幕验证

Go 标准库 + FFmpeg + 原生 WebCodecs；无 Go 第三方依赖。鼠标控制通过 cgo 调用 macOS 自带的 ApplicationServices/CoreGraphics，需要启用 cgo（macOS 默认启用）和 Xcode Command Line Tools。仅监听 `127.0.0.1`，单观看者，点击开始才采集，停止后结束 FFmpeg。默认不开启鼠标。

默认本地模式保持不变。新增主动连接的 Agent 和 HTTPS/WSS 公网 Relay，配置、双进程测试及 VPS 部署见 [RELAY.md](RELAY.md)。

Windows 10/11 x64 Agent 的实现、构建证据与实机验收步骤见 [WINDOWS.md](WINDOWS.md)。当前 Windows 状态为**待人工端到端验收**；原有 macOS 经公网 Relay 的手机浏览器控制已由用户确认。

Windows 现已接入系统软件 MF H.264，默认无需安装 FFmpeg；两文件部署与本次公网验收步骤见 [windows/MF_AGENT.md](windows/MF_AGENT.md)。独立软件编码与网页解码已由用户验证，新的Agent实时集成仍待实测。

## 运行

安装 Go、FFmpeg 和 Chrome。先在普通 macOS 终端检查设备：

```sh
ffmpeg -hide_banner -f avfoundation -list_devices true -i ''
```

此列举命令最后返回输入错误是正常的；应在其前看到 `Capture screen 0` 等设备。如果编号不同，用 `-device 'Capture screen 1'` 或设备编号。

先验证两秒硬件屏幕编码：

```sh
ffmpeg -hide_banner -f avfoundation -framerate 20 -i 'Capture screen 0:none' -t 2 -an -vf 'scale=1920:1080,format=yuv420p' -c:v h264_videotoolbox -allow_sw 0 -realtime 1 -b:v 2M -bf 0 -g 20 -f h264 -y /tmp/screen-check.h264
ffprobe -v error -count_frames -show_entries stream=codec_name,width,height,nb_read_frames /tmp/screen-check.h264
```

首次出现系统录屏提示时允许。在“系统设置 → 隐私与安全性 → 屏幕与系统音频录制”（名称随系统版本不同）中允许启动命令的终端或宿主应用，必要时彻底退出再打开它。设备枚举成功不代表已有录屏权限；还需实际编码成功、画面内容正确。不要为本项目开启麦克风权限。

```sh
go run .
```

用本机 Chrome 打开 http://127.0.0.1:8080，点击“开始”。关闭页面或点击“停止”结束采集，终端 Ctrl+C 退出服务。

配置示例：

```sh
go run . -width 1920 -height 1080 -fps 20 -bitrate 2000000 -device 'Capture screen 0' -port 8080
```

硬件失败时先看终端 FFmpeg 原始错误。`Cannot create compression session: -12908` 表示会话创建失败，不能仅凭此认定机器不支持硬件。可先用测试图排除屏幕权限因素：

```sh
go run . -test-pattern
```

只有明确指定才使用软件 H.264，绝不自动切换 JPEG、WebRTC 或软件编码：

```sh
go run . -test-pattern -encoder libx264
go run . -encoder libx264
```

## 协议和统计

- FFmpeg 固定帧率输出，禁用 B 帧，每秒一个 IDR；`h264_metadata` 插入 AUD，Go 按 AUD 组装完整访问单元，兼容三/四字节起始码、多切片和跨读取边界。
- 每个 IDR 前发送 JSON codec 配置。从 SPS 读取 profile/compatibility/level 形成 `avc1.xxxxxx`。关键帧内带缓存的 SPS/PPS，AUD 保持首位。
- 二进制消息：1 字节 IDR 标志、8 字节大端媒体 PTS（微秒）、8 字节大端 Go 收到完整编码帧时的 Unix 微秒，之后为整帧 Annex B。媒体 PTS 为帧号乘帧周期，从零开始，**不是原始屏幕捕获时间**。
- 浏览器不配置 `description`，以 Annex B 模式解码。解码异常或积压时清空解码器并等待下一个 IDR；重新点击开始可重连。
- 终端显示发送 FPS、H.264 实际负载码率和 FFmpeg 错误；页面显示接收/绘制 FPS、实际负载码率、错误数、丢弃数、解码队列、解码排队耗时及延迟的每秒均值/最大值。静止桌面实际码率低于目标是正常的，2 Mbps 是编码目标，并非固定网络流量。
- “编码帧就绪 → canvas”只估计 Go 收到完整帧后到浏览器绘制提交的时间，包含传输、排队、解码，依赖同机时钟稳定。它不含屏幕采集、编码、真实屏幕呈现；AUD 边界检测也引入约一帧等待。无法可靠报告端到端延迟、单帧编码耗时或显示器实际刷新 FPS。解码耗时包含排队，不等于纯硬件解码时间。
- WebSocket 视频仍使用原有二进制格式，配置使用 JSON；新增上行鼠标 JSON（`type: "mouse"`）和下行鼠标状态（`type: "mouse-status"`）。支持客户端 ping/pong/close。读写并发时按完整帧串行写入，慢客户端写入超时后断开。

## macOS 鼠标控制

本地屏幕采集、H.264 编码、WebSocket 视频显示、鼠标移动/点击及原生 API 执行已由用户明确确认。双击、拖动、滚动及新增公网链路仍需分别验收，不能将本地成功等同于公网成功。

建议编译到固定位置，避免 `go run` 临时可执行文件路径变化影响系统授权：

```sh
cd /Users/yuans/Desktop/远控
mkdir -p .cache
go build -o .cache/screen-mvp .
.cache/screen-mvp -check-permissions
.cache/screen-mvp -mouse -device 'Capture screen 0'
```

Chrome 访问 http://127.0.0.1:8080，点击“开始”，确认真实画面持续更新，再勾选“鼠标控制”。`-mouse` 只开启服务端控制能力，页面需手动勾选才转发鼠标事件。按 Esc、取消勾选、页面失焦或隐藏时退出控制并释放按键。

权限设置：

1. 系统设置 → 隐私与安全性 → 屏幕与系统音频录制（或屏幕录制）：允许启动程序的 Terminal/iTerm/宿主应用，实际采集由 FFmpeg 子进程完成，若系统列出 FFmpeg 也需对应允许。
2. 同一设置下的“辅助功能”：允许当前启动终端/宿主；若程序未继承该授权，可使用 `+` 添加固定的 `.cache/screen-mvp` 可执行文件（文件选择器按 Command+Shift+G 输入完整路径）。根据系统实际列出的进程授权。
3. 授权后退出并重新打开终端/宿主，再启动程序；重新运行 `-check-permissions` 查询。该命令只查询当前 Go 进程，不代表 FFmpeg 子进程一定有录屏权限；仍以实际画面和点击效果为准。
4. 无辅助功能授权时服务端不会初始化控制器，页面显示原因，视频仍可单独播放。不修改 TCC 数据、不申请管理员权限、不关闭系统防护。

屏幕和坐标规则：

- 为避免设备编号与摄像头编号混淆，鼠标模式仅接受明确的 `Capture screen N` 名称，按 FFmpeg 的活动显示器顺序映射；不支持摄像头控制或测试图控制。
- 浏览器从 canvas 内容区计算归一化坐标，扣除内部留白；CSS 缩放和浏览器缩放不需要乘设备像素比。
- macOS 使用 `CGDisplayBounds` 的全局坐标和原点，不使用编码分辨率或 Retina 物理像素。显示器断开或布局改变后结束控制，需要重新连接。多屏和不同 Retina 缩放的真实效果仍需验收。
- 移动限频约 30 次/秒，客户端发送缓冲积压时只跳过移动；按下、释放和滚轮独立发送。左右按键都保留状态，按下后的移动投递原生 dragged 事件；第二次按下/释放携带双击计数。
- 每条控制消息最多 512 字节；只接受合法掩码、完整 WebSocket 帧及已知 JSON 字段/动作，坐标必须在 0–1，按钮只支持左右，滚动量限制为 ±600 像素。非法消息终止会话并清理按键。
- 控制可用的会话每秒发送保活，5 秒未收到完整客户端消息即断开并释放；浏览器后台计时器被节流也可能触发该机制，需要重新点击开始。Ctrl+C/SIGTERM 正常退出会清理；强制杀进程、系统崩溃或系统撤销权限后，无法保证原生释放事件仍能生效。

### 人工验收顺序

1. **视频**：先不带 `-mouse` 运行，确认 Go 终端没有编码错误、画面随桌面变化、页面绘制 FPS 持续更新；记录启动参数、FPS 和码率。
2. **原生单击**：带 `-mouse` 重启，确认页面提示辅助功能已授权。先保持“鼠标控制”未勾选，在屏幕中心放置 TextEdit 等无破坏性测试目标。用 Chrome 开发者工具 Console 执行下列两条指令，确认目标发生一次实际左键单击。`x/y` 可调整为目标在整个采集屏幕中的比例。这一步用于先隔离验证原生 API，不依赖移动事件。

```js
sendMouse({action:'down',x:0.5,y:0.5,button:0,clicks:1});
sendMouse({action:'up',x:0.5,y:0.5,button:0});
```

3. **移动/缩放**：勾选控制，在视频内容中移动鼠标，检查桌面对应位置；浏览器缩放到 80%/125% 重试四角和中心，留白区域不应触发控制。
4. **点击**：在安全测试目标上检查左键单击、右键菜单、双击选择文字或打开测试文件；不要在浏览器菜单和页面按钮上测试桌面点击。
5. **拖动**：在画面内按住标题栏移动、释放，检查窗口跟随并停止；另测拖出画面后释放。
6. **滚动**：在可滚动的测试文档中向上/向下滚轮，确认方向和幅度。
7. **清理**：按住后按 Esc、切到其他窗口、隐藏页面、停止播放、关闭标签页或断开连接，分别确认拖动结束；重新连接后不应残留按下状态。

**本机自控限制**：浏览器与被控桌面共享同一个系统鼠标。注入移动会移动本地指针，可能将指针移出浏览器或再次触发浏览器事件；点击其他窗口会使浏览器失焦，并按要求主动释放/停用控制。因此，同一台 Mac 上直接手动拖动的测试可能被失焦或指针反馈打断。先完成上面的单次原生点击验证；将这种测试干扰与权限失败区分。本地模式保留失焦保护；新增公网模式请使用另一台设备上的浏览器验收完整控制体验，见 [RELAY.md](RELAY.md)。

## 验证状态

```sh
go test -v ./...
go build .
node mouse_test.cjs
```

当前开发环境已验证 Go 编译、Annex B 分帧单测、libx264 1080p/20 FPS 两秒 40 帧编码，以及进程内 WebSocket 输出协议测试。测试图与软件编码仅用于诊断。

2026-10-30，用户反馈本机实测功能正常，屏幕视频链路已获用户实测确认。未另行记录实测启动参数及性能数据，不据此宣称硬件编码或某一性能指标已单独验证。

开发沙箱内仍无法绑定本地端口，AVFoundation 未列出屏幕，VideoToolbox 返回 -12908，浏览器自动化也未能稳定启动。这些是开发环境中的限制记录；本机实测结果以上述用户反馈为准。

2026-10-31 复查：Go 1.25.2、FFmpeg 9.0.2；编译、`go vet`、`go test -race`、视频测试、鼠标输入校验/掩码协议测试及进程内真实握手/视频与 pong 并发测试通过。Node 测试覆盖坐标、CSS 缩放、内部留白、DPR、移动限频、点击/释放不被限频丢弃、双击计数、右键、滚轮与失焦清理。这些测试没有注入原生鼠标事件，也没有运行浏览器解码。

本次开发环境检查仍得到端口绑定 `operation not permitted`、屏幕设备列表为空、硬件编码会话 -12908；开发进程的录屏/辅助功能查询均为 false。没有重复尝试受限操作或扩大权限。这些结果仅代表开发沙箱，不代表用户本机进程的授权状态。

2026-10-31，用户反馈本机已能移动鼠标，**鼠标移动已获用户实测确认**。用户仅有本机测试环境；“点击应该没问题”属于推测，因此左键、右键、双击、拖动、滚动及失焦/断线释放仍标记为待实测。当前版本视频播放未单独记录新的验收数据，保留此前视频版本的实测结论。

参考：https://etducky.com/blog/webcodecs-h264-remote-desktop

原生 API 与屏幕枚举参考：[Apple CGEvent 字段](https://developer.apple.com/documentation/coregraphics/cgeventfield)、[FFmpeg AVFoundation 实现](https://github.com/FFmpeg/FFmpeg/blob/master/libavdevice/avfoundation.m)。

新增 Relay 前，用户进一步明确确认本地视频链路、鼠标移动/点击及 macOS 原生鼠标执行正常。公网 Relay 的验证状态和待验收项目单独记录于 [RELAY.md](RELAY.md)。
