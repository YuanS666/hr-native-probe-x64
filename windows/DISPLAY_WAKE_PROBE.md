# 已熄屏恢复：独立硬件验证

修订4用户实测未通过：SetThreadExecutionState返回成功，DuplicateOutput仍E_ACCESSDENIED。不能声称已修复。原请求还会随每次失败的子进程释放；本探针先持续持有请求排除该因素，再用独立显式开屏命令验证，不预先改造Agent。

Windows自带PowerShell可运行，无需VS、FFmpeg、管理员、修改执行策略或安装新组件。若组织策略阻止脚本，不绕过策略。

1. 关闭AnyDesk隐私模式，保留原1分钟关闭屏幕、睡眠从不；Agent正常运行。将test-display-wake.ps1保存到Windows。
2. 在普通Windows PowerShell执行 `powershell -NoProfile -File .\test-display-wake.ps1`，立即断开AnyDesk；至少75秒内不通过其他软件发送输入。
3. 75秒时A阶段持续申请显示器开启/保持20秒，95秒B阶段显式开屏，随后保持20秒并释放。不要在这115秒内重连AnyDesk，否则污染结果。若Agent已耗尽重试，可在我们的浏览器点一次停止/开始，但记录时间，不触碰Windows本机鼠标。
4. 保存完整探针输出和Agent原生日志；说明恢复发生在A、B还是均未恢复。两阶段有20秒时间间隔，若驱动延迟恢复不能仅靠一次结果判断因果，需重复对照。

只请求Default普通桌面的显示电源，不解锁、不注入鼠标，不改变锁屏策略；实体屏幕可能亮起。手工关闭/杀死PowerShell时线程结束撤销其线程请求。API返回成功不等于面板亮起；桌面查询和发送之间仍存在系统切换竞态，但开屏命令不携带输入/解锁权限。

源码依据：
- https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-setthreadexecutionstate
- https://learn.microsoft.com/en-us/windows/win32/menurc/wm-syscommand （SC_MONITORPOWER，lParam=-1）
- https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendmessagetimeoutw （单窗口1500ms，避免HWND_BROADCAST按窗口累计超时）

当前环境无Windows PowerShell/.NET及真实显示器，未执行此探针，语法与原生行为待Windows实测。本次不生成第二个未经验证的Agent修复包，修订4不能作为黑屏恢复通过版本。若两阶段均无效，下一步应调查显示电源通知与驱动/DXGI状态，而不是继续堆叠唤醒或重试。

交互粘贴兼容更新：去掉脚本级param声明，固定75秒等待和每阶段20秒，避免param不位于交互输入起始位置的解析错误。重新测试应使用新窗口，避免旧类型已加载冲突。Windows端仍待执行验证。

2026-11-01审计更新：另开窗口运行observe-display.ps1，只读观察180秒显示off/on/dim、锁定和输入桌面；新版探针用环境变量RD_DISPLAY_TEST_MODE选择baseline（不做任何显示请求）、hold（只持续显示请求40秒）、wake（默认，A/B各20秒）。例如先设置 `$env:RD_DISPLAY_TEST_MODE='hold'` 再正常运行脚本。不再需要编辑C#或参数声明。CI只解析和编译，真实行为待Windows测试。
