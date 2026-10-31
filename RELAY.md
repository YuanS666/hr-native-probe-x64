# 公网 Relay

当前阿里云 CentOS 7 / LNMP / `erp.beiaiying.com` 的端口方案和部署进度见 [DEPLOY_CENTOS7.md](DEPLOY_CENTOS7.md)。该环境使用独立 8443，下面的通用 443 服务模板不能直接照搬。

沿用现有项目和同一个二进制：`-mode local`（默认）保持原本的本地服务；`-mode agent` 在 Mac 主动出站；`-mode relay` 在 Linux VPS 或 Mac 提供 HTTPS/WSS 中转。没有新增第三方 Go 依赖。Relay 不需要 FFmpeg、屏幕或辅助功能权限。

```text
Mac Agent ──WSS──> Go Relay <──HTTPS/WSS── 浏览器
```

## 配置与认证

| 环境变量 | 使用方 | 含义 |
| --- | --- | --- |
| `RELAY_DEVICE_ID` | 两端 | 唯一设备 ID，1–64 个字母/数字/横线/下划线，也可用 `-device-id` |
| `RELAY_AGENT_KEY` | 两端 | Agent 的随机认证密钥，至少 32 字节 |
| `RELAY_VIEWER_KEY` | Relay | 浏览器的不同随机密钥，至少 32 字节；用户在 HTTPS 表单中输入 |
| `RELAY_PUBLIC_URL` | Relay | `https://relay.example.com`，也可用 `-public-url` |
| `RELAY_URL` | Agent | `wss://relay.example.com/agent`，也可用 `-relay` |
| `RELAY_TLS_CERT` | Relay | 完整证书链 PEM，也可用 `-tls-cert` |
| `RELAY_TLS_KEY` | Relay | 证书私钥 PEM，也可用 `-tls-key` |

Agent 用 `Authorization: Bearer ...` 和设备 ID 认证。浏览器输入设备 ID 和浏览器密钥后，获得绑定该设备的签名 Cookie（Secure、HttpOnly、SameSite=Strict）；密钥不写进 HTML、JavaScript、URL 或浏览器本地存储。这里以持有不同角色密钥作为身份，浏览器密钥仅授予配置的这一个设备的控制权，没有用户管理系统。

浏览器授权 30 分钟有效，到期主动结束会话，刷新页面重新认证。每次控制连接生成新的随机会话 ID，Relay 和 Agent 都校验控制消息的设备、会话、类型和鼠标范围。Agent 密钥和浏览器密钥必须不同，轮换密钥后重启 Relay。

Relay 仅提供直接 HTTPS/WSS：缺少证书时拒绝启动，不接受明文 WS 或通过 `X-Forwarded-Proto` 冒充 TLS。Agent 验证证书和主机名，没有跳过校验选项。TLS 在 Relay 终止，Relay 能读取编码数据，但不解码、不重新编码、不保存视频；因此 VPS 本身属于可信组件。

## 先在 Mac 上用两个进程测试

### 一次性准备

在本机普通终端运行：

```sh
cd /Users/yuans/Desktop/远控
mkdir -p .cache/relay-test
go build -o .cache/screen-mvp .
umask 077
printf 'export RELAY_DEVICE_ID=mac1\nexport RELAY_AGENT_KEY=%s\nexport RELAY_VIEWER_KEY=%s\n' \
  "$(openssl rand -hex 32)" "$(openssl rand -hex 32)" > .cache/relay-test/keys.env
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout .cache/relay-test/localhost.key \
  -out .cache/relay-test/localhost.crt \
  -subj /CN=localhost \
  -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
  -addext 'basicConstraints=critical,CA:TRUE'
```

只需生成一次密钥，后面的两个进程读取同一份文件。`.cache` 已被 Git 忽略。将这个本地测试证书导入 macOS“钥匙串访问”的登录钥匙串，并显式信任该测试证书，然后重新打开 Chrome。访问时应正常通过证书验证，不能靠忽略证书错误或关闭浏览器安全检查来测试。测试完成后可以移除这张测试证书；公网使用正规域名证书。

### 终端 A：Relay

```sh
cd /Users/yuans/Desktop/远控
source .cache/relay-test/keys.env
.cache/screen-mvp -mode relay \
  -listen 127.0.0.1:8443 \
  -public-url https://localhost:8443 \
  -tls-cert .cache/relay-test/localhost.crt \
  -tls-key .cache/relay-test/localhost.key
```

### 终端 B：Mac Agent

```sh
cd /Users/yuans/Desktop/远控
source .cache/relay-test/keys.env
.cache/screen-mvp -check-permissions
.cache/screen-mvp -mode agent \
  -relay wss://localhost:8443/agent \
  -ca-file .cache/relay-test/localhost.crt \
  -mouse -device 'Capture screen 0'
```

原有 `-width`、`-height`、`-fps`、`-bitrate` 和编码器配置同样有效，默认继续使用 VideoToolbox。录屏和辅助功能授权仍按主 README 操作，使用固定的 `.cache/screen-mvp` 路径。

### Chrome

打开 **https://localhost:8443/**，设备 ID 填 `mac1`，浏览器密钥取 `keys.env` 的 `RELAY_VIEWER_KEY`。可在读取了该文件的终端中执行以下命令供自己复制，密钥不要发到聊天或日志中：

```sh
printf '%s\n' "$RELAY_VIEWER_KEY"
```

认证后点击“开始”，确认视频后勾选鼠标控制。需要精确使用 `localhost:8443`，不能换成与配置不同的来源。先测试视频、单击，再测试右键、双击、拖动和滚轮；同机测试仍受共享系统指针和失焦保护影响，跨网络浏览器才可完整隔离控制端与被控端。

如果旧版本登录时提示“来源无效”，重新编译并重启 Relay，再从 `https://localhost:8443/` 强制刷新后提交，不能直接刷新 `/login` 错误页面。已修复旧版本 `Referrer-Policy: no-referrer` 导致表单 POST 的 `Origin` 变为 `null` 的问题；当前使用 `same-origin`，仍严格拒绝空来源与跨站请求。

原本的本地模式保持不变：

```sh
.cache/screen-mvp -mouse
```

本地模式地址仍为 **http://127.0.0.1:8080/**。Agent 模式不会启动这个监听端口。

## iPhone 局域网测试：192.168.10.112

原有 localhost 证书不匹配 Mac 的局域网 IP，手机也不会继承 Mac 的证书信任。已在当前工作目录生成独立测试 CA 和服务器证书，位于 `.cache/relay-test/lan-192-168-10-112/`；没有覆盖原有证书和认证密钥。服务器证书有效两天，包含 `192.168.10.112`、`localhost` 和 `127.0.0.1`，证书链、服务器用途和 IP 验证已通过；手机实际连接仍待人工确认。

1. 手机与 Mac 连接同一个可互通的 Wi-Fi。先停止旧 A、B 进程。
2. 将 `iphone-ca.cer` 通过 AirDrop 传给手机并安装（系统设置中的已下载描述文件或“通用 → VPN 与设备管理”），再到“通用 → 关于本机 → 证书信任设置”开启对 `Screen MVP LAN Test CA` 的完全信任。只传这个公开 CA 证书，不传 `.key` 文件或 `keys.env`。
3. A、B 分别使用下面的命令，确保监听地址、公开来源、Agent 地址和证书一致。

终端 A：

```sh
cd /Users/yuans/Desktop/远控
source .cache/relay-test/keys.env
.cache/screen-mvp -mode relay \
  -listen 192.168.10.112:8443 \
  -public-url https://192.168.10.112:8443 \
  -tls-cert .cache/relay-test/lan-192-168-10-112/fullchain.pem \
  -tls-key .cache/relay-test/lan-192-168-10-112/server.key
```

保持 A 运行，再启动终端 B：

```sh
cd /Users/yuans/Desktop/远控
source .cache/relay-test/keys.env
.cache/screen-mvp -mode agent -mouse \
  -relay wss://192.168.10.112:8443/agent \
  -ca-file .cache/relay-test/lan-192-168-10-112/ca.crt
```

手机 Safari 打开 **https://192.168.10.112:8443/**，使用 `keys.env` 中的设备 ID 和浏览器密钥登录。手机上的 `localhost` 指手机自身；蜂窝网络不能直接访问这个局域网地址，公网连接仍需要 VPS。

`tls: bad certificate` 出现在 Relay 时，通常表示对端拒绝了服务器证书，不代表 Relay 必然退出；Agent 的 `connect: connection refused` 则表示它尝试连接的地址没有可达的监听服务。若 B 仍被拒绝，先检查 A 是否还运行、是否报端口占用或地址绑定错误，并确认 Mac 当前 IP 仍为 `192.168.10.112`。

测试完成后可在手机移除这个临时 CA。公网继续使用正式域名证书。iPhone 触摸拖动、右键和滚动适配尚未完成，此步骤仅用于验证加密连接和后续视频播放。

证书信任设置参考：[Apple 官方说明](https://support.apple.com/zh-cn/102390)。

## 部署到 Linux VPS

以下以使用 systemd 的 Debian/Ubuntu 和 `relay.example.com` 为例，替换为你自己的域名和 VPS 地址。DNS 指向 VPS，按现有云安全组/防火墙规则放行 TCP 443；若使用 HTTP-01 签发证书，还需开放 TCP 80。Mac 只需允许出站 TCP 443，不做端口映射。

### 编译并上传

在 Mac 项目目录编译 Linux Relay（amd64 VPS；ARM64 VPS 将 `GOARCH` 改为 `arm64`）：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o .cache/screen-relay-linux .
scp .cache/screen-relay-linux YOUR_USER@YOUR_VPS:/tmp/screen-relay
```

VPS 上执行：

```sh
sudo useradd --system --home /opt/screen-relay --shell /usr/sbin/nologin screen-relay
sudo install -d -m 755 /opt/screen-relay
sudo install -d -m 750 -o root -g screen-relay /etc/screen-relay
sudo install -m 755 /tmp/screen-relay /opt/screen-relay/screen-relay
```

若服务用户已经存在，沿用该用户即可。VPS 不需要安装 Go、FFmpeg 或 macOS 框架。

### TLS 证书

使用已有的有效域名证书，或通过 ACME 工具签发。若已安装 Certbot 且 80 端口可用于临时验证：

```sh
sudo certbot certonly --standalone -d relay.example.com
sudo install -o root -g screen-relay -m 640 \
  /etc/letsencrypt/live/relay.example.com/fullchain.pem /etc/screen-relay/fullchain.pem
sudo install -o root -g screen-relay -m 640 \
  /etc/letsencrypt/live/relay.example.com/privkey.pem /etc/screen-relay/privkey.pem
```

Certbot 是可选证书工具，不是程序运行依赖；安装方法见 [Certbot 官方说明](https://certbot.eff.org/instructions?ws=other&os=pip)。已有证书则直接将完整证书链和私钥安装到上述路径。

### 密钥及服务配置

在 VPS 运行两次 `openssl rand -hex 32`，分别生成不同的 Agent 密钥和浏览器密钥。用 `sudoedit /etc/screen-relay/relay.env` 写入下列配置，替换两项密钥和域名：

```ini
RELAY_DEVICE_ID=mac1
RELAY_AGENT_KEY=替换为独立的随机Agent密钥
RELAY_VIEWER_KEY=替换为独立的随机浏览器密钥
RELAY_PUBLIC_URL=https://relay.example.com
RELAY_TLS_CERT=/etc/screen-relay/fullchain.pem
RELAY_TLS_KEY=/etc/screen-relay/privkey.pem
```

```sh
sudo chmod 600 /etc/screen-relay/relay.env
```

创建 `/etc/systemd/system/screen-relay.service`：

```ini
[Unit]
Description=Single-device screen relay
After=network-online.target
Wants=network-online.target

[Service]
User=screen-relay
Group=screen-relay
WorkingDirectory=/opt/screen-relay
EnvironmentFile=/etc/screen-relay/relay.env
ExecStart=/opt/screen-relay/screen-relay -mode relay -listen :443
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now screen-relay
sudo systemctl status screen-relay
sudo journalctl -u screen-relay -n 30 --no-pager
```

Go 自己提供 TLS，不需要 Nginx、Docker 或其他转发框架。443 若已被其他服务占用，可统一改用 `-listen :8443`、`RELAY_PUBLIC_URL=https://relay.example.com:8443` 和 Agent 对应的 8443 地址，并放行该端口。

证书续期后，重复复制证书和私钥的命令，再执行 `sudo systemctl restart screen-relay`；可将相同步骤配置为 ACME 工具的 deploy hook。当前进程不会自动重读证书文件，轮换证书或密钥都需要重启。重启会断开当前浏览器。

### Mac 连接公网 Relay

Mac 只配置 Agent 密钥，浏览器密钥只交给控制者。在普通终端输入 Agent 密钥（不回显），然后启动：

```sh
cd /Users/yuans/Desktop/远控
export RELAY_DEVICE_ID=mac1
printf '粘贴 Agent 密钥后回车：'
read -rs RELAY_AGENT_KEY
export RELAY_AGENT_KEY
printf '\n'
.cache/screen-mvp -mode agent -mouse -relay wss://relay.example.com/agent
```

公网正规证书使用系统信任库，不传 `-ca-file`。控制浏览器访问 **https://relay.example.com/**，输入 `mac1` 和浏览器密钥，点击开始后勾选鼠标控制。HTTPS 页面自动使用 WSS。

## 公网验收步骤

1. Mac 在家庭/办公网络运行 Agent，控制浏览器连接另一个网络（例如另一台电脑使用手机热点）。确认 Relay 和 Agent 日志显示认证连接，浏览器证书无错误。
2. 验证桌面变化、视频 FPS、实际码率，然后验证鼠标移动、左右键、双击、窗口拖动和滚动。跨机单向视频延迟因时钟未同步显示为不可可靠测量，解码/排队耗时仍可统计。
3. 用错误的浏览器密钥或设备 ID 登录，应拒绝访问；错误 Agent 密钥不能占用设备。打开第二个控制窗口，应得到设备占用错误（WebSocket 握手 HTTP 409），原会话继续工作。
4. 关闭第一个控制窗口，再在新窗口连接，应立即建立新会话并从新编码器的 IDR 显示，不出现缺少参数集的解码错误。
5. 拖动过程中关闭控制标签页，Mac 应释放鼠标。浏览器断开时 Relay 立即发送 stop，Agent 先释放按键再等待 FFmpeg 结束。真实网络失联只能在保活/读写超时后检测，不能保证物理断网瞬间知晓。
6. 保持按下时停止 Relay 或断开 Agent 网络，确认 Agent 清理按键；恢复网络后观察 1、2、4、8、16、30 秒封顶的重连退避。连接稳定超过 30 秒后恢复从 1 秒开始退避。浏览器不会自动重新取得控制，需点击“开始”；认证到期则刷新重新登录。
7. 降低可用带宽或让控制页面暂停执行，观察会话被终止并清理按键，恢复后重新连接。检查没有持续增长的视频队列；需要长期低于目标带宽运行时，手动降低 Agent 的 `-bitrate` 或 `-fps`，本项目不做自适应码率。

## 限制与清理策略

- 仅一台 Agent、一个控制浏览器，最多 16 个底层连接（包含 HTTP 和 TLS 握手）。HTTP 头限制配置为 8 KiB；登录体最大 1 KiB，控制消息最大 1 KiB，其中鼠标 JSON 最大 512 字节；视频及会话外层最大 16 MiB + 64 字节。
- HTTP 读头 5 秒、读写 10 秒、空闲 30 秒；WebSocket 写入 3 秒、完整帧读取 10 秒。两端每 3 秒 ping，浏览器每秒发应用保活，5 秒没有有效应用消息即关闭控制会话。后台标签页计时器节流也可能触发断开。
- Relay 同步转发单帧，不建立视频队列。写超时终止整个浏览器会话并通知 Agent；不在同一参考链中随意丢弃预测帧。严重反压也可能令 Agent 写入超时，此时 Agent 释放按键后进入重连退避。重连重启 FFmpeg 产生新 IDR；浏览器解码积压时重置解码器并等待下个周期 IDR（默认最多约一秒），不会将预测帧标成关键帧。
- 浏览器关闭、认证过期、非法消息、Agent 掉线或 Relay 退出都触发清理。通知失败则断开 Agent，使其走本地释放路径。系统强制杀死 Mac 进程、系统崩溃或撤销辅助功能权限后，无法保证原生释放事件仍能生效。
- 不保存视频，不做多设备界面、数据库、键盘、音频、文件传输或 WebRTC。首次连接需要启动 FFmpeg，有启动延迟。

## 本次可执行验证

```sh
go test -race -count=1 ./...
go vet ./...
node mouse_test.cjs
go build -o .cache/screen-mvp .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o .cache/screen-relay-linux .
```

已在开发环境完成代码检查、构建和自动化测试。进程内测试使用真实 TLS 和 HTTP/WebSocket 握手、真实 FFmpeg/libx264 编码，覆盖角色/设备认证、Cookie 篡改/过期、跨站拒绝、单控制者、新会话 SPS/PPS/IDR、旧会话拒绝、慢浏览器超时和模拟鼠标释放。还验证了明文 WS 被拒绝、未信任证书导致握手失败，以及前端会话外层与保活逻辑。

开发沙箱仍拒绝 `127.0.0.1:8443` 监听，未继续尝试真实端口、屏幕或公网。因此**没有完成两个进程的浏览器播放、真实原生鼠标中转、VPS 部署、跨公网操作或真实断网自动重连验收**。原本的本地视频、移动、点击和原生鼠标执行以用户已确认的结果为准；自动化测试不能替代新链路的真实验收。

参考：[RFC 6455](https://www.rfc-editor.org/rfc/rfc6455)、[Go HTTPS 服务接口](https://pkg.go.dev/net/http#Server.ServeTLS)。
