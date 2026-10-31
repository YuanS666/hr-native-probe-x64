# 独立 Windows DXGI + Media Foundation H.264 探针

状态：源码已实现；macOS 上的可移植码流测试通过，**Windows 原生构建、采集和 WebCodecs 播放待实机验证**。本程序没有接入 Agent，不连接 Relay，不采集音频、不执行鼠标操作，不修改现有 macOS 或生产浏览器。

## 先构建，再复制一个 EXE

**本机没有 VS 或配置较低：优先用 GitHub Actions 云构建。** 源码包内 `.github/workflows/build-native-probe.yml` 使用预装 VS/SDK 的 `windows-2022`。新建一个 GitHub 仓库，将解压后的 `index.html`、`windows/`、`.github/` 上传到仓库根目录并提交，不能只上传 ZIP。Mac Finder 如未显示 `.github`，按 Command+Shift+句号。随后打开 Actions → Build Windows Native Probe，查看自动任务或点击 Run workflow。构建成功后在该次运行底部 Artifacts 下载 `windows-native-probe-x64`，其中包含 EXE、依赖清单、SHA256和构建状态。

此配置本地已准备，**尚未在 GitHub 执行**。云端不运行真实采集，编译或测试失败时不会发布成功产物，应回传失败步骤日志继续修复。下载后在本机直接运行以下软件/硬件测试命令即可。公有仓库标准runner免费，私有仓库取决于账号Actions额度；不需要上传Relay密钥或桌面录像。只上传探针源码包列出的文件即可。

构建机需要 Windows x64、Visual Studio 2022/Build Tools 的“使用 C++ 的桌面开发”、Windows SDK（建议10.0.19041+）和 CMake。VS 安装器可勾选“适用于 Windows 的 C++ CMake 工具”。这些都是**构建依赖**，目标机不需要。

源码包保留 `index.html` 和 `windows/native-probe/` 的目录关系，构建时嵌入现有页面与独立回放脚本。不要只拷贝 probe.cpp。

打开 **x64 Native Tools Command Prompt for VS 2022**，进入解压后的 `windows\native-probe`，执行：

```bat
cmake -S . -B build -A x64
cmake --build build --config Release
ctest --test-dir build -C Release --output-on-failure
dumpbin /dependents build\Release\screen_native_probe.exe
```

得到 `build\Release\screen_native_probe.exe`。CMake 使用 Release `/MT`，没有 FFmpeg、Python、Rust 或 Go 链接依赖。用 `dumpbin` 核对：不应出现 avcodec/avutil/ffmpeg、VCRUNTIME/MSVCP 等额外可分发 DLL；系统 API DLL、API-set 和显卡驱动组件仍是系统运行依赖。实际清单和干净机器运行结果才是最终证据。

只把该 EXE 复制到目标 Windows 的一个可写目录即可运行，不需要复制源码、HTML、CSV或SDK。探针运行后自行生成网页和结果。目标：Windows 10 1703+ / Windows 11 x64，普通物理控制台已登录，主屏处于 SDR、无旋转；正常显卡驱动和系统媒体组件可用。Windows N 可能需要系统 Media Feature Pack。不要以管理员身份启动，不操作锁屏/UAC安全桌面；不支持RDP、HDR及副屏切换。

## 第一次测试：先系统软件 H.264

在普通 PowerShell 中进入 EXE 所在目录。先检查构建命令全部成功，再执行：

```powershell
.\screen_native_probe.exe --encoder software --seconds 10 --output software 2>&1 | Tee-Object software.log
```

运行时移动窗口、滚动普通网页，让桌面实际变化。成功应看到：

```text
selected Microsoft H264 software MFT; GPU conversion + CPU readback, no FFmpeg
...
COMPLETE: encoded=... IDR=... bytes=... submit-to-output mean/P95/max=...
```

这是预期日志模式，**不是本机已测日志**。只有看到实际 `COMPLETE`、进程退出码0且浏览器测试通过才算此阶段通过。`FAILED` 后的残留文件不能算成功。程序拒绝覆盖，重试请换 `--output software2` 等新前缀。

双击生成的 `software.html`，用 Chrome/Edge 打开，点击“验证真实 MF 视频（两次回放）”。该网页嵌入了本次实际 MF 视频，并调用当前项目原有 `message()` / `VideoDecoder` / `draw()` 函数；不访问网络。它依次验证完整解码、中途重置后等待真实 IDR 恢复。最终应显示 `PASS`，同时目视确认录到的是真实桌面、颜色和黑边正确。

若 `VideoDecoder` 或安全上下文不可用，保留错误，不关闭浏览器安全设置。此次文件回放不代表实时公网成功，更不代表端到端延迟通过。

## 第二次测试：硬件与 auto

```powershell
.\screen_native_probe.exe --encoder hardware --seconds 10 --output hardware 2>&1 | Tee-Object hardware.log
.\screen_native_probe.exe --encoder auto --seconds 60 --output auto60 2>&1 | Tee-Object auto60.log
```

硬编只枚举主屏 adapter LUID 对应的 MFT，检查 D3D11 aware、异步事件、NV12输入和Baseline输出。`hardware` 不回退；不支持就明确失败。`auto` 只在初始化阶段可回退系统软件 MFT，日志会写明；一旦开始编码，失败便终止，避免把两个编码器的流混在同一文件中。硬件名称、CLSID、LUID 和拒绝属性 HRESULT 均写入日志，不申请管理员权限或修改注册表。

分别打开 `hardware.html`、`auto60.html` 验证实际解码。若硬件失败而软件成功，请回传两份日志；不要自行安装 FFmpeg 作为后备。

## 参数及输出

```powershell
.\screen_native_probe.exe --encoder auto --seconds 10 --output native-test --width 1920 --height 1080 --fps 20 --bitrate 2000000
```

- 默认1080p、20fps、2Mbps、10秒。输出宽高需偶数且不超过3840×2160，fps为1–60，时长2–120秒，H.264最大64MiB，未完成输入队列最多8帧。此探针用于短时验证，暂不承担1小时稳定性测试。
- 时长限制采集提交窗口，随后最多等待10秒drain；编码过慢时实际帧数可少于时长×FPS，不能据此声称达到目标帧率。
- `.h264`：真实 Annex B，逐AU加AUD，IDR带SPS/PPS；拒绝B切片、多帧sample、首帧非IDR、缺参数集、非单调PTS。系统序列头按Annex B解析，不盲猜AVCC。
- `.html`：本次实际码流的独立回放页，原协议17字节头中的ready固定为0，离线模式不拿它计算延迟。
- `-timing.csv`：每帧PTS（100ns）、IDR、字节数和提交到输出可用的实际QPC耗时。20fps的标称duration为500000；采集/编码变慢时PTS保留实际时间缺口，不靠高速补帧制造达标数据。
- `-stats.csv`：累计真实新桌面帧、重复帧、提交/输出数，以及每采样窗口实际输出FPS、H.264负载Mbps、CPU占整机百分比、WorkingSet、PrivateBytes、匹配本进程PID的GPU引擎最大占用。

CPU为本进程用户+内核CPU时间差除以实际经过时间和逻辑处理器数，包含GPU回读、文件写入和Base64网页生成的开销；不能直接当作最终Agent的CPU性能。硬编也有有限GPU复制，不承诺驱动内部零复制。为简化生命周期，每个输入分配独立NV12纹理，不做复杂纹理池。

GPU字段分别为VideoEncode、VideoProcessing、Copy、3D的本进程计数器最大值，不是整机总GPU占用；驱动未提供、查询失败或未找到计数器时CSV**留空**，不能解释为0%。硬件工作可能由其他进程或驱动归属，需要结合任务管理器/ETW核查。

`submit_to_output_ms`包含排队与MFT输出处理，不等于纯硬件编码耗时。GPU转换的纯GPU执行时间、屏幕到屏幕端到端延迟**未测量**。桌面静止时DXGI可能没有新图，程序以最后自有纹理编码，`new_desktop_frames`与`repeated_frames`分别记录，不将重复编码帧冒充新采集帧。

## 错误与验收边界

- 缺媒体组件：`MFStartup` 或创建Microsoft编码器报HRESULT；Windows N检查系统Media Feature Pack。
- `DuplicateOutput` / `AcquireNextFrame` 失败：检查是否锁屏、RDP、显示布局变化；本探针明确退出，修复后手动重启，无无限重启循环。
- 显卡不支持NV12 VideoProcessor转换：软件模式也会失败，因为软件模式仍验证DXGI+GPU转换；不会悄悄改用GDI。记录HRESULT继续定位。
- `CBR` / `B frames` / `low latency` / 媒体类型协商失败：保留具体属性和HRESULT。关键参数拒绝就失败，不能仅凭“有H264编码器”推测该路径可用。
- 编码无进展/未完成帧超过5秒、drain超过10秒：有界等待失败退出；驱动API内部卡死不受用户态轮询超时保证，不能承诺处理驱动级挂起。
- 没有合成独立鼠标指针；DXGI是否已包含指针与驱动有关，当前只验桌面视频。正式接入前再实现/验收指针合成。

请回传：软件/硬件日志、浏览器PASS或错误截图，以及对应timing/stats CSV；并提供Windows版本、显卡/驱动、主屏分辨率和DPI。H264/HTML包含真实桌面内容，只在你愿意分享时提供。

本次可执行的本机检查：AppleClang可移植码流单元测试及40帧真实libx264固定流规范化通过，JavaScript语法检查通过。它们不代表Windows API构建成功、MF编码成功或实际WebCodecs解码成功。
