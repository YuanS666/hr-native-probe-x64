# 键鼠控制与网络恢复

当前Go Agent修订5、Windows helper修订7。基础键盘已有用户实测；小键盘等具体待验收项见下文。生命周期与当前修订见[AUDIT_CHANGES.md](AUDIT_CHANGES.md)。

## P0：释放责任

输入控制器的release现在返回错误。失败后原控制器由唯一进程内清理任务保留，每秒只补发旧状态的UP；成功前拒绝建立新控制器。任务不依赖浏览器或WSS连接，状态不形成无界列表。Windows仍只访问本机console的Default桌面，不绕过UAC；Mac仍要求辅助功能权限。

清理成功日志：“旧会话释放事件已提交”。Windows远程会话通过保活重新检查权限；Mac/本地模式可在清理成功后重新开始观看。用户主动退出/强制结束整个Agent后，内存清理任务不能继续，不能保证已经退出的进程在解锁后补发UP。Quartz CGEventPost没有执行确认，Mac结果仍需真实目标应用验证。

## P1：基础物理键盘

服务端必须显式添加 `-keyboard`，鼠标仍由 `-mouse` 单独控制。浏览器勾选“键盘控制”后点击画面，只有画面拥有焦点时才转发；其他网页控件和表单不转发。按键按照被控端键盘布局解释，不自动将Mac Command映射为Windows Ctrl。

支持：小键盘0–9、加减乘除、小数点、Enter、Equal、NumLock，以及A–Z物理键、数字行、常见标点、空格、Enter、Tab、Backspace、Delete、Home/End、PageUp/PageDown、方向键、F1–F12及左右Shift/Ctrl/Alt/Meta。Meta在Windows对应Win键，在Mac对应Command。支持浏览器产生的按键重复，慢连接只允许丢弃重复事件，不丢初次DOWN或UP。

不支持：中文/IME组成文本传输、手机软键盘文本、CapsLock/ScrollLock等其他锁定切换键、任意Unicode文本、剪贴板同步。远端中文输入法可能接收物理键，但没有做兼容性承诺。Ctrl+Alt+Delete、安全桌面及UAC不支持。操作系统及浏览器保留快捷键（如Alt+Tab、Command+Q、Ctrl+W等）可能由控制端截获，不能保证传到远端。

本地Esc始终停止全部控制。需要让远端程序接收Esc时，使用页面“发送远端Esc”按钮。失焦、隐藏、停止、连接断开和采集暂停都会释放鼠标和键盘，恢复连接不会自动勾选输入，更不会恢复旧按下状态。

沿用已有control外层中的 `mouse` 字段承载输入对象：对象type仍区分mouse/keyboard，原鼠标消息不变。键盘只接受down/up/release及物理code白名单；未知字段、类型、设备、会话、越界参数仍拒绝。没有第二条控制连接。

## P1：浏览器网络恢复

仅用户已开始观看时自动重连，间隔1/2/4/8/16/30秒，六次失败后停止。真正收到绘制画面且连接存活超过30秒后才重置预算。后台页面暂停重连，回到前台继续。主动停止会取消定时器；已经在途的认证响应不能重新开启用户停止的连接。

认证预检最多等待10秒，主动停止会中止在途预检。HTTPS重连前使用 `/session` 查询原Cookie，204表示仍有效，401提示刷新重新认证。不会自动延长或绕过现有30分钟授权。实际恢复时间包含浏览器、Agent各自退避以及新IDR，不等于网络RTT或画面延迟。

## 一次更新

当前完整Windows部署说明见[windows/MF_AGENT.md](windows/MF_AGENT.md)。本轮helper进度停滞分类需要revision7，使用同次Actions产出的双EXE，不再使用已过期的Go-only输入更新包。

若希望完整重新构建：上传完整源码到原GitHub仓库，Actions → Build Windows Agent → Run workflow。更新Windows产物中的两个EXE及VPS Relay；新版页面、键盘类型校验和/session都在Relay内，因此只更新Windows不足以启用P1。旧密钥/设备ID/证书均保留。浏览器强制刷新。

Windows解压目录PowerShell（占位设备ID换成现有值）：

```powershell
$secret = Read-Host '现有Agent密钥' -AsSecureString
$env:RELAY_AGENT_KEY = [System.Net.NetworkCredential]::new('', $secret).Password
.\screen-agent-windows-amd64.exe -mode agent -relay wss://erp.beiaiying.com:8443/agent -device-id '现有设备ID' -mouse -keyboard 2>&1 | Tee-Object input-test.log
```

Mac本地构建并沿用现有录屏、辅助功能授权和FFmpeg安装：

```sh
go build -o screen-agent .
./screen-agent -mode agent -relay wss://erp.beiaiying.com:8443/agent -device-id '现有设备ID' -mouse -keyboard
```

Mac终端需已有RELAY_AGENT_KEY环境变量，不把密钥写入脚本/参数或日志。另一个Agent仍在线时先正常停止，Relay继续只接受一个Agent和一个浏览器。访问原地址 https://erp.beiaiying.com:8443/ ，使用原观看密钥。

## 回来后的最小人工验收

1. 先不勾键盘，确认视频及鼠标移动、左右键、拖动和滚轮不回归。
2. Windows记事本/Mac文本编辑中勾选键盘，点击画面，输入abc、Shift+A、数字和标点；测试Enter、Backspace、方向键及持续按住字母。Windows测试Ctrl+A/C/V，Mac测试Command+A/C/V（只用远端自己的剪贴板，不是跨机同步）。
3. 按住修饰键或拖动时切换控制端窗口，再返回：旧按下必须释放，不能继续拖动或带着Ctrl/Shift输入。按本地Esc后控制开关均关闭；重新勾选后“发送远端Esc”可关闭远端普通菜单/对话框。
4. Windows在有本机解锁入口时，拖动/按键期间锁屏，约10秒后解锁。检查“输入清理暂停/全部释放”日志、无残留按键。此项必须真实Windows验证；不能用模拟测试代替。权限被撤回时恢复权限后再操作。
5. 本机短暂断网约5秒再恢复，浏览器视频应有限重连；恢复后输入仍关闭，重新勾选可控制。不必重跑独立编码探针。
6. 点击停止后即使网络恢复也不得自动播放。切换后台后回来应恢复视频连接尝试；Cookie过期后新建/恢复连接应提示重新认证；现有活跃连接不因30分钟硬切，失去保活仍应清理。

记录通过/失败、平台、目标应用及input-test.log。系统保留快捷键/安全桌面、IME不作为本次支持项。真实Windows/Mac输入、锁屏和公网重连均待这轮人工验收；本机逻辑/TLS管道测试和编译结果见随包验证记录。

源码/API依据：Windows键盘布局与返回值按微软KEYBDINPUT/SendInput文档处理；Mac物理键码已逐项对照本机构建SDK Events.h的99个常量。CGEventPost不提供执行确认，编译与此表校验不代替真实输入验收。

- https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-keybdinput
- https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput
- https://developer.apple.com/documentation/coregraphics/cgevent/init(keyboardeventsource:virtualkey:keydown:)

## 数字小键盘补充修订

用户已实测普通按键、方向键及复制粘贴等快捷键基本正常；此前小键盘被白名单过滤，已补齐前端、Relay、Agent共同白名单。需同时更新VPS Relay和目标Agent，再强制刷新浏览器；数字区本身不依赖helper，但当前统一交付使用revision7。

NumLock不会自动同步或被强制开启。按一次NumLock只切换被控Windows一次，长按重复不再转发。输入数字时请确认被控端NumLock开启；关闭时数字区可能执行End/Home/方向键等，这是目标Windows根据锁定状态的正常解释。Mac目标上同一物理NumLock位置对应Clear，不具有Windows数字锁定语义。小数点字符由被控端布局决定，可能为点或逗号。

最小验收：远端记事本输入0123456789、+ - * /、小数点和小键盘Enter；测试NumLock开/关及长按，确认状态不被每帧强制修改；NumLock关闭时验证1/End、2/下箭头、0/Insert等语义；按住数字后失焦应停止重复。新小键盘效果仍待真实Windows反馈。

扫描码依据：[微软Scan Codes表](https://learn.microsoft.com/en-us/windows/win32/inputdev/about-keyboard-input#scan-codes)。

当前VPS程序路径沿用 `/home/wwwroot/erp.beiaiying.com/screen-relay-release/screen-relay`，Relay启动参数仍为 `-mode relay -listen :8443`，不加-keyboard；-keyboard只用于目标Agent。密钥、证书及启动环境不变。
