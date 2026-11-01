# 当前交接（2026-11-01）

工作区：/Users/yuans/Desktop/远控。只做最小必要修改，新增函数中文注释，不引入未经授权的第三方依赖，不自动部署/推送。当前没有Git元数据。原用户文本“新建文本文档.txt”不清理。

## 现状及入口

- Mac：FFmpeg AVFoundation + VideoToolbox，CGO Quartz输入；Windows：DXGI/D3D11 + 系统软件MF H264，SendInput；Go Agent→WSS Relay←WebCodecs/Canvas浏览器。单设备单控制者。
- Go Agent修订5，C++helper修订7；Windows Actions生成双EXE。运行/部署入口：README.md、windows/MF_AGENT.md、RELAY.md、DEPLOY_CENTOS7.md。
- 用户实际VPS二进制路径/home/wwwroot/erp.beiaiying.com/screen-relay-release/screen-relay。保留原服务/密钥/证书，不把/opt模板当作实际路径，不给Relay/systemctl加-keyboard。
- 鼠标、基础键盘及公网链路已由用户验证，小键盘完整实测仍待反馈；旧Windows修订曾连续20分钟无断开。

## 当前最重要的未完成项

Windows一体机Intel HD Graphics630、1920×1080、睡眠从不、显示1分钟自动关闭后，DuplicateOutput持续E_ACCESSDENIED，session=console=3，Default桌面。实体鼠标亮屏或AnyDesk重连后恢复；关闭AnyDesk隐私模式后仍复现。修订4显示请求尝试没有解决“已经黑屏后的恢复”。探针运行期间持续亮屏/控制正常，但不能区分A/B阶段效果。

按用户的架构审计结论，先实验不继续盲改Agent唤醒：windows/observe-display.ps1只记录显示off/on/dim、WTS锁定和输入桌面；test-display-wake.ps1通过RD_DISPLAY_TEST_MODE选择baseline/hold/wake；说明DISPLAY_WAKE_PROBE.md。无Windows GUI/.NET环境，诊断C#编译仅安排CI，真实事件和熄屏仍待用户验证，不可声称修复完成。

## 最近修改和验证

详见AUDIT_CHANGES.md：helper5秒进度停滞退出75，可恢复；坏数据仍永久错误。Relay不再按Cookie30分钟硬切已认证活跃连接，过期Cookie不能新建/恢复，5秒应用保活清理仍生效。新增Relay写入P50/P95/max及吞吐诊断；慢viewer、半开Agent、Cookie过期测试和WS fuzz。Mac新增-ffmpeg显式路径，默认不变。

全量race通过123.542秒，fuzz5秒57268次；便携C++、Node逻辑、三平台Go构建和vet通过。完整Windows原生helper/PowerShell编译及实机/公网验收未执行。慢链路仍可能有界拆会话，不盲目改成丢帧；未来优化项遵循审计的测量门槛。

## 交付与清理

.cache保留最新audit-update-source.zip、linux-relay-audit-update.zip、mac-agent-audit-update-arm64.zip、windows-display-audit-tools.zip；windows-agent-watchdog-update.zip和windows-agent-stability-r6.zip为旧回退基线。证据和清理记录JSON保留。Go编译缓存及临时产物已删除，不为文档清理重跑全量构建以重建缓存。

本轮删除6份重复或过期阶段文档，未删除仍有效的Go文件；构建文档引用已同步。最新源包同步当前文档，旧回退ZIP保持不改。不要按旧聊天链接查找已删除的阶段文档，当前入口以README和AUDIT_CHANGES为准。
