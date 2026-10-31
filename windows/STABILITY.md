# Windows DXGI 稳定性修订 revision 6

状态：Go测试/构建与可移植C++测试通过；Windows原生编译、DXGI故障恢复和真实持续运行待验收。没有新的运行时或Go第三方依赖。

## 已有证据与未证实的原因

- 用户日志证明先发生AcquireNextFrame的DXGI_ERROR_ACCESS_LOST，之后DuplicateOutput持续E_ACCESSDENIED，稍后新进程又能初始化。用户检查只有一个Agent、一个采集进程，均在活动console会话3。因此不能认定重复进程、服务会话或AnyDesk是根因。
- 官方DuplicateOutput文档将E_ACCESSDENIED定义为无法访问当前桌面图像；不能据此推断一定锁屏/UAC。Adapter不匹配通常是E_INVALIDARG。旧代码初始化时本已从同一Adapter创建Device及Output，没有证据证明初始匹配错误。
- 原正常路径Acquire成功后会Release，异常路径也会Release，未发现确定的正常帧泄漏。旧恢复仅Reset Duplication，并复用Output、D3D设备与转换资源，无法覆盖设备丢失及显示拓扑更新。这是恢复范围不足，尚不能证明它导致了最初的拒绝。
- 原Agent在编码进程退出后return，必然关闭WSS；这一故障耦合已由源码和测试确认。

源码依据（实际函数）：

- [微软Desktop Duplication API](https://learn.microsoft.com/en-us/windows/win32/direct3ddxgi/desktop-dup-api)：AcquireNextFrame、桌面表面与超时语义。
- [DuplicateOutput返回值及Adapter契约](https://learn.microsoft.com/en-us/windows/win32/api/dxgi1_2/nf-dxgi1_2-idxgioutput1-duplicateoutput)。
- [微软DuplicationManager.cpp](https://github.com/microsoft/Windows-classic-samples/blob/main/Samples/DXGIDesktopDuplication/cpp/DuplicationManager.cpp)：InitDupl从设备取Adapter并枚举Output；GetFrame/DoneWithFrame处理Acquire与Release。
- [微软DesktopDuplication.cpp](https://github.com/microsoft/Windows-classic-samples/blob/main/Samples/DXGIDesktopDuplication/cpp/DesktopDuplication.cpp)：CreateDuplicationExpectedErrors将E_ACCESSDENIED列为预期切换错误；DDProc/DDProcCore区分预期/永久失败，DYNAMIC_WAIT退避。示例线程会绑定输入桌面；本项目仅接受当前console的WinSta0/Default，不照搬GENERIC_ALL或切到安全桌面。

## 当前机制

原生每次启动只初始化一次，采集和释放均在主线程。日志包含phase/API/HRESULT、PID/session/console/TID、窗口站、线程与输入桌面名、当前主屏/显示器数、Adapter LUID/Output矩形、holding_dup/holding_frame、init/cleanup/acquired/returned、last_success_age_ms。

Acquire成功才设置持帧标志；资源引用释放后归还一次。析构依次释放帧、视图、纹理、Duplication、Output、转换上下文和Device。初始化部分失败由COM成员自动析构并记录init-cleanup。强制取消时可能没有析构日志，Go仍等待进程退出回收，不能把缺少日志当作资源泄漏证据。

| 情况 | 行为 |
|---|---|
| WAIT_TIMEOUT | 正常等待；首次没有画面不伪造黑帧，已有自有纹理可重复 |
| ACCESS_LOST、E_ACCESSDENIED、DEVICE_REMOVED/RESET、SESSION_DISCONNECTED | 记录现场；退出码75请求受限恢复 |
| 无已连接主屏、非Default输入桌面或OpenInputDesktop暂时不可用 | 不捕获、不切桌面，退出码75进入暂停 |
| 分辨率或主输出改变 | 退出码75；新进程重新枚举匹配的Adapter/Output，重建D3D及像素转换 |
| 未分类故障、缺系统组件、永久不支持格式 | 退出码1，不自动重试 |

Go只在旧子进程Wait完成后启动下一个。Windows MF使用现有WSS发capture-status：starting/paused/running/stopped。最多六次恢复，间隔1/2/4/8/16/30秒（累计等待61秒，不含每次初始化耗时）。只有收到首帧并持续存活至少一分钟后才重置预算，短暂成功不会无限刷新预算。恢复耗尽保留WSS和会话，页面明确显示stopped；用户检查原因后可停止/重新开始，不重新登录（认证未过期时）。

暂停释放鼠标并拒绝操作；恢复首个IDR后重新查询权限和显示布局。若系统拒绝原生释放，会记录失败，不能保证安全桌面中的注入成功。设备、会话、消息类型/长度和坐标仍先校验，非法输入会断连，不因暂停而放宽。

每个新进程从新IDR、SPS/PPS和PTS零开始；暂停/starting消息先重置浏览器解码器。Relay继续原封装转发，不解码、不储存，转发及认证源码未修改。慢客户端写超时、真实网络故障和授权到期仍可能断开WSS。

## 一键构建和部署

没有VS的电脑：上传完整源码到原GitHub仓库后，Actions → **Build Windows Agent** → **Run workflow**。只运行这一项；下载windows-agent-mf-x64和linux-relay-amd64产物。不要只上传ZIP文件。

有构建工具的Windows：Go 1.25+、VS 2022 C++ Build Tools（x64编译器、Windows SDK、CMake工具；不需要完整IDE），仓库根目录运行：

```powershell
powershell -NoProfile -File .\windows\build.ps1
```

脚本失败即停止，不继续打包。生成：

- release/windows-agent-mf-x64.zip：screen-agent-windows-amd64.exe、screen_capture.exe、依赖报告、哈希和说明。
- release/linux-relay-amd64.zip：screen-relay及更新说明。

脚本与CI共用一个入口；浏览器Node测试额外由CI执行。当前macOS没有PowerShell/Windows SDK，不能声称此脚本已在Windows执行通过。

Windows替换两个EXE；VPS也必须更新Relay二进制（新版状态处理HTML内嵌其中），沿用环境、证书、设备ID和密钥，不改ERP站点配置。若systemd配置确为项目默认路径，上传新screen-relay到/tmp/screen-relay.new后：

```sh
sudo systemctl cat screen-relay
sudo install -m 755 /tmp/screen-relay.new /opt/screen-relay/screen-relay.new
sudo cp -p /opt/screen-relay/screen-relay /opt/screen-relay/screen-relay.previous
sudo mv /opt/screen-relay/screen-relay.new /opt/screen-relay/screen-relay
sudo systemctl restart screen-relay
sudo systemctl status screen-relay --no-pager
```

路径不符时按实际ExecStart替换。服务重启会暂时断开现有连接；完成后浏览器强制刷新HTTPS页面。

Windows普通PowerShell运行（用真实设备ID替换占位）：

```powershell
$secret = Read-Host '现有Agent密钥' -AsSecureString
$env:RELAY_AGENT_KEY = [System.Net.NetworkCredential]::new('', $secret).Password
.\screen-agent-windows-amd64.exe -mode agent -relay wss://erp.beiaiying.com:8443/agent -device-id '现有设备ID' -mouse 2>&1 | Tee-Object agent-r6.log
```

访问 https://erp.beiaiying.com:8443/ ，使用原观看密钥。启动确认revision=6；不要公开密钥或录入日志。

最终电脑仅需Windows 10/11 x64、可用显示驱动/DXGI/D3D11及系统Media Foundation H264软件编码器；N/KN版本需安装系统Media Feature Pack。C++静态CRT（/MT），Go禁用CGO。目标无需FFmpeg、Python、Go、VS、CMake或独立VC运行库。仍为两个EXE，不增加复杂单文件封装。

## 真实Windows验收（全部待人工执行）

先确认只有一个Agent和一个screen_capture，保持普通console桌面。B/C/D只能在有本机操作和恢复入口的情况下进行，避免断屏后无法解锁。

| 项目 | 操作与检查 |
|---|---|
| A 连续30分钟 | 同机以 `.\screen-agent-windows-amd64.exe -mouse` 运行，Chrome打开 http://127.0.0.1:8080 ，持续31分钟并间歇移动窗口。记录FPS、码率、CPU、内存是否持续增长。此项仅测持续采集/编码，不覆盖Agent恢复状态机。公网Relay既有30分钟授权期限保留，不能把它到期关闭当DXGI异常，也不能用重新登录拼接成30分钟连续公网验收。公网单次30分钟以上仍受原策略限制，标记未验证。 |
| B 锁屏/解锁 | 公网播放时Win+L，本机等待10秒后解锁。预期先paused、释放鼠标，不出现新的“已通过WSS连接”；回到Default后starting→running，出现新IDR/画面并可重新控制。若超过预算，预期stopped，需要显式重新开始。 |
| C 分辨率 | Windows显示设置更改到另一受支持SDR分辨率并保留；预期完整重建、重新打印Adapter/Output及content矩形，画面恢复，四角/中心点击无旧映射。输出仍固定配置1080p并等比留白。再改回。 |
| D 显示器 | 仅有可操作外接屏时拔插；内屏电脑无外接设备可记录“不适用”。记录主输出变化，恢复后核对坐标。 |
| E 重启Agent | Ctrl+C后确认子进程退出；用同一命令重启；网络连接重建属于预期，浏览器可能需重新开始，未过期无需重新输入密钥。不得留下重复进程。 |
| F 短暂断网 | 本机断网5秒再恢复；真实网络断线允许WSS重连，旧按钮必须释放，Agent按现有1–30秒退避，浏览器必要时重新开始，检查新IDR和控制。 |

性能采样可在另一个普通PowerShell执行（产物已附validate.ps1）：

```powershell
powershell -NoProfile -File .\validate.ps1 -Mode perf -AgentProcessId (Get-Process screen-agent-windows-amd64).Id -DurationSeconds 1800
```

每秒记录Agent及其screen_capture子进程的CPU/working set/private bytes；进程更换后按新PID重新计算CPU，未测得CPU的首个样本留空，不填伪造值。

每次故障记录：操作时间、phase/API/hr、PID/session/TID与桌面名、Adapter/Output、holding_frame与acquired/returned、CAPTURE_EXIT、Go回收日志、paused/starting/running/stopped及新IDR后画面恢复时间。`running`时间减去`paused`时间是软件恢复耗时，不是端到端画面延迟；以浏览器真实画面恢复为最终结果。

每项填写：通过/未通过/不适用、恢复耗时、是否人工重新开始、峰值CPU/内存、对应日志。失败时保留agent-r6.log，尤其是首次错误发生前后的诊断，不必重跑独立编码探针。

性能以原生日志实际FPS/码率/CPU/working set及任务管理器观察；浏览器看解码队列。进程退出后原生内存由系统回收。跨进程内存增长和30分钟稳定性需要真实Windows数据。GPU引擎读数、编码耗时及玻璃到玻璃延迟本次没有新实测；RTT不代替画面延迟。
