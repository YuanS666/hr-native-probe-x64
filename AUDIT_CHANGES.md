# 架构审计逐项处理（2026-11-01）

依据用户提供的《远程桌面项目架构审计报告.md》。本轮保持技术路线，不添加远程桌面功能。代码已改不等于Windows或公网实测通过。

## 按项结果

| 报告事项 | 处理及文件 | 当前状态 |
|---|---|---|
| P0 熄屏恢复先做实验，不继续改Agent | 新增windows/observe-display.ps1，订阅GUID_CONSOLE_DISPLAY_STATE，记录off/on/dim、WTS锁定/解锁、session/console及input_desktop；与旧A/B探针并行。补脚本解析和C#编译CI | 观测工具已写；Windows编译和实验待执行，不称熄屏已修复 |
| P1 helper停滞错误归类 | progress.hpp将5秒整体/最旧输入停滞分类为CaptureStall；probe.cpp独立捕获退出75，其他std::exception仍1。原有Go看门狗/六次恢复预算不变 | 便携C++边界测试和Go真实子进程退出分类通过；完整Windows helper需Actions构建 |
| P1 活跃会话30分钟硬断 | relay.go改为连接建立时校验Cookie有效期，已认证连接按现有5秒应用保活管理，不在Cookie到期时切断 | TLS真实通道测试跨Cookie到期继续；过期Cookie新/ws和/session仍401；停止保活关闭 |
| P1 慢链路先测量 | 新增video_forward_stats.go，Relay每秒输出同步视频写入P50/P95/max和净负载FPS/Mbps；120样本上限、截断标记、按会话重置。保留同步转发与失败兜底 | 有界不读TLS viewer测试已执行；真实netem/带宽和光学延迟待测 |
| P1 慢viewer/半开Agent/WS fuzz | 新增relay_resilience_test.go和websocket_fuzz_test.go；纳入两平台CI筛选。真实3秒写/5秒保活等生产阈值不缩短 | 竞态测试通过；5秒fuzz执行57268次，无崩溃或契约违反，不是完整安全证明 |
| P2 ffmpeg显式路径 | main.go增加-ffmpeg，默认仍ffmpeg，不更改现有Mac默认编码；Mac闭stdout子进程用绝对路径+空PATH测试 | 代码及自动测试；launchd真实录屏/权限待验收 |
| 以后再优化 | s.mu持锁网络写、帧池复用、光标、Job Object、CSP/HMAC派生、隐藏页停解码、可选关键帧请求及应用层降级 | 不在缺少测量时改动；不是已完成 |
| 不需要改变 | DXGI+软件MFT、FFmpeg/VideoToolbox、WSS、Annex B+WebCodecs、原生键鼠、单设备、标准库和原生helper | 保留，不引入WGC/SCK/WebRTC/驱动 |

## 授权策略的精确定义

本轮选择“建连授权 + 已连接会话空闲/断线清理”，没有新增自动续签接口：

- Cookie仍30分钟，Host/Origin/设备/签名检查完全保留。过期Cookie不能新建或恢复连接，需重新登录。
- 在Cookie有效时建立的连接可继续观看及控制，不再30分钟硬切。连接没有固定总时长上限，持续有效应用保活代表会话仍在使用。
- 用户停止、5秒未收到合法控制/观看保活、Agent断开或Relay退出仍结束会话并清理输入。
- 此变化不是无限延长HTTP登录授权，也不会跨断线保留旧按下状态。需要绝对总会话上限或主动撤销机制时应另定策略，不暗中实现。

## 已观察到的慢客户端边界

测试使用net.Pipe+真实TLS/认证WebSocket；读取welcome后完全不再读视频，代表零缓冲极端反压，不是公网吞吐基准。

- 视频写约3000ms超时，Relay停止旧参考链。
- 如果同时回复Agent ping又写stop，零缓冲通道可能再等待一次3秒写超时；无法确认stop送达时按现有逻辑关闭Agent，由断连触发输入释放。
- 测试要求“stop确实送达”或“Agent确实关闭”，不是收到一条超时日志就通过；整体必须在两次写截止加调度余量内清理。
- 半开Agent不读不回复测试约6秒被心跳写失败清理，重复Agent此前409，之后可连接。不是所有真实半开网络都固定6秒（缓冲/读超时不同）。
- 没有擅自加入按帧丢弃，也没有缩小TCP缓冲以制造好看的数字。当前慢链路仍可能拆会话，这项优化待真实测量决定。
- 新Relay写耗时包含写锁/TLS/内核等待，不包含浏览器收包解码显示；短写耗时也不能证明无网络积压，必须同时观察ss Send-Q。写入分位数不是端到端延迟。

## Windows电源实验：现在要做什么

本次没有把A/B探针开屏命令接入Agent；修订4熄屏恢复仍未通过。最新“脚本开着期间持续亮屏/可控制”是线索，不区分哪个API有效。

新增观察器运行180秒，不产生显示电源请求；观察器与探针在不同PowerShell窗口运行。每次用新窗口避免已加载类型冲突。若系统策略允许本机运行脚本，正常使用 `powershell -NoProfile -File .\observe-display.ps1`；若组织策略拦截，不更改/绕过策略。

先运行观察器，再进行各组：
1. 基线：停止观看、让显示器按原1分钟超时熄灭，再仅开始观看，记录显示off/on和DXGI成功/失败。
2. 仅请求显示：先设置 `$env:RD_DISPLAY_TEST_MODE='hold'` 再运行新版探针；仅持续显示请求，不发送B命令，记录是否从已off恢复。不能把A+B结果冒充“仅A”。
3. 请求+开屏：设置 `$env:RD_DISPLAY_TEST_MODE='wake'` 后运行完整A/B探针，记录A/B阶段及显示状态，结合Agent进程PID、HRESULT和首帧日志。
4. 官方DDA/WGC示例：仅在前三组无定论时，由有真实Windows开发环境者做对照，不引入生产Agent。

初筛每组5次；候选方案再做10次，成功至少9/10才考虑集成。不要用5次样本声称9/10成功率。未收到显示事件时记录unknown，不推断为亮/灭；显示dim不等于锁屏。WTS flags映射针对Windows10/11，x64 ABI前缀检查，不支持旧Win7反转语义。

会话期间防空闲熄屏单独测试：亮屏开始观看3分钟不输入，powercfg /requests查询DISPLAY请求；停止观看后请求消失、恢复原计划。观察器本身不执行powercfg或改变计划。

官方依据：
- [显示状态订阅](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-registerpowersettingnotification)
- [off/on/dim定义](https://learn.microsoft.com/en-us/windows/win32/power/power-setting-guids)
- [WTS锁定标志](https://learn.microsoft.com/en-us/windows/win32/api/wtsapi32/ns-wtsapi32-wtsinfoex_level1_w)

观察器和探针脚本已加入Windows CI仅解析/编译，不执行Run，也不让CI机器亮屏。当前Mac没有Windows PowerShell/.NET GUI，不能本地验证C#运行。不要把CI编译当成实机电源事件测试。

## 慢链路测量步骤（专用Linux测试环境，非生产VPS）

保持1080p20FPS/2Mbps，固定同一滚动网页，记录基线、单向延迟50ms、1%丢包、1.5Mbps限速各120秒，每组3次。先用现有Relay日志+浏览器JSON+ss采样，按会话分组P50/P95；光学延迟方法见PERFORMANCE_V1.md。

只有专用测试网卡、可以安全恢复并有本地控制台时，才使用netem。不要在生产VPS、SSH管理网卡或已有复杂qdisc上直接覆盖。

```sh
# TEST_IFACE必须是你已经确认的专用测试网卡；先查看已有配置。
tc qdisc show dev "$TEST_IFACE"
# 以下三种条件分别测试，每次仅添加一种，add失败时不使用replace覆盖旧配置。
sudo tc qdisc add dev "$TEST_IFACE" root handle 10: netem delay 50ms
# 采样后只删除本次handle10的测试qdisc，再测试下一种。
sudo tc qdisc del dev "$TEST_IFACE" root handle 10:
# 其余两种条件分别为 netem loss 1% 和 netem rate 1500kbit。
```

[netem手册](https://man7.org/linux/man-pages/man8/tc-netem.8.html)指出定时器粒度等会影响限速行为；单接口规则只影响其发出方向，不等于双向RTT。真实带宽/丢包/画面延迟未在本轮执行，不能根据零缓冲测试决定最终队列大小。

## 文件和部署

生产改动：relay.go、video_forward_stats.go、main.go（-ffmpeg）；Windows原生probe.cpp及新增progress.hpp。测试/诊断：relay_resilience_test.go、websocket_fuzz_test.go、ffmpeg_watchdog_test.go、bitstream_test.cpp、observe-display.ps1、check-display-diagnostics.ps1、test-display-wake.ps1。CI/build.ps1加入测试和文档，未重设计构建流程。

- VPS：只更新原screen-relay，原路径/home/wwwroot/erp.beiaiying.com/screen-relay-release/screen-relay，保留实际服务配置/证书/密钥；先备份，用原服务停止→替换→启动。不要加-keyboard。生效后看到Relay写入统计；活跃连接不再被30分钟期限切断。
- Windows：本次进度超时归类改在C++，必须用既有Actions重新构建一次完整双EXE包（helper revision7），仅替换Go EXE不会获得它。没有修改MF编码参数或显示唤醒行为；不能期待这次包解决熄屏。
- Mac：默认原命令仍有效；launchd或后台环境可指定 `-ffmpeg /opt/homebrew/bin/ffmpeg`（Apple Silicon常见路径，仅示例；用实际command -v ffmpeg结果）。本次不迁移采集API。

本机预编译包为Go1.25.2测试产物，正式部署优先沿用现有Actions的1.25.x或服务器现有较新工具链构建，避免无意降级。没有自动部署到VPS或推送GitHub。

## 本轮自动验证证据

- `GOCACHE="$PWD/.cache/go-build" go test -race -count=1 -timeout 4m ./...`：通过，screen-mvp 123.542秒（包括完整协议、授权、输入、H264固定流和子进程测试）。
- `go test -race ... TestRelaySlowViewer/TestRelayHalfOpenAgent/TestRelayActiveAuthorization`：通过，25.420秒；不是公网带宽或端到端性能测试。
- `go test -run '^$' -fuzz '^FuzzReadWSFrame$' -fuzztime 5s -parallel 1 -timeout 20s .`：通过，57268次执行、29条新增有意义输入，无发现失败。
- `c++ -std=c++17 -Wall -Wextra -pedantic windows/native-probe/bitstream_test.cpp ...`及执行：通过，新增5秒整体/最旧输入精确边界和异常类别检查，原有AU测试保留。
- `node mouse_test.cjs`、`node input_browser_test.cjs`：通过，含324组坐标与重连/诊断逻辑；未执行真实系统输入或浏览器H264解码。
- Linux amd64/Windows amd64/Mac arm64 Go构建、Windows测试二进制交叉编译、Mac/Windows vet：通过。
- Windows完整C++ helper与PowerShell/C#诊断编译：已安排既有CI，当前没有SDK/.NET，未在本机执行，未声称Actions已通过。
- Windows显示电源实验、实际VPS慢链路、30分钟真实活跃连接及长时性能：未验证，需人工验收。
