# Windows Agent：构建、部署与验收

当前源码：Go Agent修订5，原生采集helper修订7。DXGI/D3D11 + 系统软件Media Foundation H.264，默认1920×1080、20 FPS、2 Mbps。硬件MFT仅探针实验，生产Agent不依赖FFmpeg。

## 构建与文件

GitHub Actions → **Build Windows Agent** → Run workflow。下载同一次成功任务的 **windows-agent-mf-x64**，保留：

```text
screen-agent-windows-amd64.exe
screen_capture.exe
```

两文件放同一目录，只启动Go Agent。目标机无需FFmpeg、Python、Go或VS；需要Windows10/11 x64、正常显卡驱动与系统媒体组件，Windows N可能需Media Feature Pack。当前支持已登录的物理控制台、SDR主屏，不支持安全桌面/RDP/HDR/旋转显示。

构建机才需要Go、VS2022 C++ Build Tools、Windows SDK及CMake。已有构建流程不变：

```powershell
.\windows\build.ps1
```

生成release/windows-agent-mf-x64.zip和release/linux-relay-amd64.zip。编译、原生bitstream测试及无GUI协议测试不代表真实采集成功。revision7将编码5秒进度停滞改为退出75并走Go有限重建；仅换Go EXE不会更新这项行为。

## 公网运行

退出旧Agent，沿用原设备ID及密钥；同一Relay仅接受一个Agent。不要把Agent密钥与浏览器查看密钥混用。

```powershell
$secret = Read-Host '现有Agent密钥' -AsSecureString
$env:RELAY_AGENT_KEY = [System.Net.NetworkCredential]::new('', $secret).Password
.\screen-agent-windows-amd64.exe -mode agent -relay wss://erp.beiaiying.com:8443/agent -device-id '现有设备ID' -mouse -keyboard
```

浏览器访问 https://erp.beiaiying.com:8443/ ，用浏览器密钥认证。鼠标、键盘分别勾选；键盘需画面焦点。原生输入受UIPI限制，不提权、不绕过UAC或锁屏。

本地模式（不经Relay）：

```powershell
.\screen-agent-windows-amd64.exe -mouse -keyboard
```

Chrome打开 http://127.0.0.1:8080/ 。本机自控共用鼠标，失焦保护可能停用控制；完整操作建议用另一台设备验收。

## 恢复机制和已知限制

- starting/paused/running/stopped通过原WSS报告。可恢复采集错误最多六次完整重建，间隔1/2/4/8/16/30秒；预算耗尽停止，不无限高速重启。
- 首帧15秒、之后8秒无完整编码帧，由Go终止并回收子进程。停止观看/断线时释放本会话按键；释放失败跨连接保留补释放责任。
- Cookie30分钟限制新建/恢复连接，已认证活跃连接不在到期时硬切；5秒应用保活丢失仍结束会话。
- **熄屏恢复仍未解决。**显示电源请求API成功后，这台Intel HD Graphics630一体机仍可能DuplicateOutput=0x80070005。脚本保持亮屏时控制正常，不能据此证明已黑屏可恢复。当前Agent仍在子进程生命周期申请/释放显示请求。
- 用户已验证公网视频、鼠标及基础键盘，旧修订曾20分钟无断开；新版helper、熄屏和长时间性能须分别实测。

当前修改与证据见随包AUDIT_CHANGES.md，源码中为[审计处理说明](../AUDIT_CHANGES.md)；键鼠细节见[INPUT_UPDATE.md](../INPUT_UPDATE.md)。

## 最小验收

1. 检查日志Go修订5/native-capture revision7；视频与左/右键、双击、拖动、滚轮、基础键盘正常。
2. 停止观看后确认采集子进程回收。断网5秒恢复后画面重新更新，输入不自动勾选。
3. 连续播放30分钟并记录FPS、码率、资源、错误；不要把重复画面当新桌面更新数，不用RTT代替画面延迟。
4. 锁屏/解锁、分辨率变化及可适用的显示器重连分别记录恢复与按键释放，不期待绕过安全桌面。
5. 熄屏实验使用[显示探针说明](DISPLAY_WAKE_PROBE.md)和observe-display.ps1；baseline/hold/wake分组记录，不在缺少实测时更换采集技术栈。

九点、资源与光学延迟辅助工具仍为validate.ps1、nine-points.js、latency-pattern.html；完整测量方法见[PERFORMANCE_V1.md](../PERFORMANCE_V1.md)。
