> 当前公网已由用户实测跑通。实际二进制为 `/home/wwwroot/erp.beiaiying.com/screen-relay-release/screen-relay`；更新沿用现有服务配置，不重新执行下方首次安装模板。当前行为见[AUDIT_CHANGES.md](AUDIT_CHANGES.md)。

# 当前阿里云 VPS 部署记录

已由用户提供：CentOS Linux 7、x86_64、Go 1.25.5 linux/amd64；Nginx 占用 80/443，现有应用监听 127.0.0.1:8080，MySQL 使用 3306，8443 暂未使用。目标域名为 `erp.beiaiying.com`。

## 部署方案

- 独立目录 `/opt/screen-relay`，独立 systemd 服务 `screen-relay`。
- Relay 自己提供 TLS，监听 `:8443`；浏览器地址为 `https://erp.beiaiying.com:8443`，Agent 地址为 `wss://erp.beiaiying.com:8443/agent`。
- 现有 Nginx、80/443 站点、8080 应用和 MySQL 保持原样。
- 使用该域名有效的完整证书链和私钥，复制到 Relay 专用目录后供独立服务用户读取。已确认配置路径为 `/usr/local/nginx/conf/ssl/erp.beiaiying.com_bundle.crt` 和 `/usr/local/nginx/conf/ssl/erp.beiaiying.com.key`；证书内容、有效期及私钥匹配由安装器在 VPS 检查，不能直接采用本机 localhost 或局域网测试证书。
- 阿里云 ECS 安全组需要允许入站 TCP 8443；如主机防火墙正在运行，也需在其实际活动区域添加 8443。先检查现状，不关闭防火墙或 SELinux。

## 已确认的只读检查

在 VPS 运行下面的命令，输出系统服务版本及目标站点证书的配置路径，不读取私钥文件内容：

```sh
systemctl --version
sudo awk '/^[[:space:]]*(server_name|ssl_certificate|ssl_certificate_key)[[:space:]]/ {print}' \
  /usr/local/nginx/conf/vhost/erp.beiaiying.com.conf
```

用户已确认 systemd 219 及上述证书路径。以下部署不修改网站配置文件。

## 安装包部署

安装包为 `.cache/screen-relay-centos7.tar.gz`，只包含 Linux 程序、证书检查器、安装脚本、服务配置和校验和，不包含认证密钥、私钥或视频。

Mac 新开终端上传，替换为平时 SSH 登录使用的用户名和 VPS 公网 IP：

```sh
cd /Users/yuans/Desktop/远控
scp .cache/screen-relay-centos7.tar.gz SSH用户名@VPS公网IP:screen-relay-centos7.tar.gz
```

VPS 终端执行：

```sh
umask 077
mkdir -p ~/screen-relay-release
tar -xzf ~/screen-relay-centos7.tar.gz -C ~/screen-relay-release
cd ~/screen-relay-release
sudo bash install-centos7.sh
```

安装器会先检查文件完整性、程序可运行性、TLS 域名、证书有效期和私钥匹配，再创建独立用户和目录。首次生成两套新随机密钥并写入 root-only 的 `/etc/screen-relay/relay.env`；重复安装保留已有认证配置。检查器仅验证内部链与基础配置，客户端是否信任签发根仍需实际 HTTPS 验证。

安装器只停止/重启本项目的 `screen-relay.service`，不重新加载 Nginx，不改网站、数据库或其他服务。不自动改安全组、主机防火墙或 SELinux。

## 开通 8443 和检查服务

阿里云 ECS 控制台中，为该实例的安全组新增入方向 TCP 8443 规则，允许 Mac 和控制浏览器所在网络访问。若需要任意蜂窝网络连接，可按实际需求配置公网来源；认证仍由 Relay 的 TLS 和密钥强制执行。

如果 firewalld 正在运行，先检查实际活动区域：

```sh
sudo firewall-cmd --get-active-zones
```

仅当活动区域确认为 `public` 时，添加运行时与永久规则：

```sh
sudo firewall-cmd --zone=public --add-port=8443/tcp
sudo firewall-cmd --permanent --zone=public --add-port=8443/tcp
```

其他区域按实际名称替换；若 firewalld 未运行或命令不存在，不为此启动另一套防火墙，继续检查当前实际使用的规则。

```sh
sudo systemctl status screen-relay --no-pager -l
sudo journalctl -u screen-relay -n 30 --no-pager
curl --resolve erp.beiaiying.com:8443:127.0.0.1 \
  -sS -o /dev/null -w '本机 HTTPS HTTP %{http_code}\n' \
  https://erp.beiaiying.com:8443/
```

最后一个命令应在证书校验通过后得到 HTTP 200，它只验证 VPS 本机服务，不替代公网验证。不要使用 `curl -k` 跳过证书验证。外部连接还需域名 DNS 指向该 VPS；若域名经过 CDN，需确认该入口支持 8443，否则使用直接指向 VPS 且有有效证书的域名。

## Mac 和 iPhone 公网连接

VPS 上仅自行查看密钥并复制，勿把输出发到聊天：

```sh
sudo sed -n 's/^RELAY_AGENT_KEY=//p' /etc/screen-relay/relay.env
sudo sed -n 's/^RELAY_VIEWER_KEY=//p' /etc/screen-relay/relay.env
```

第一项用于 Mac，第二项用于浏览器，均不是之前本地 `keys.env` 中的测试密钥。首次部署默认设备 ID 为 `mac1`；已有配置则以原文件为准。

先停止本机 A、B 的局域网测试进程。Mac 终端执行，`read` 时粘贴 VPS 的 Agent 密钥并回车，输入不回显：

```sh
cd /Users/yuans/Desktop/远控
printf '粘贴 VPS 的 Agent 密钥：'
read -rs RELAY_AGENT_KEY
export RELAY_AGENT_KEY
printf '\n'
.cache/screen-mvp -mode agent -mouse -device-id mac1 \
  -relay wss://erp.beiaiying.com:8443/agent
```

公网使用系统正常信任的域名证书，不传本地测试 `-ca-file`。iPhone 关闭 Wi-Fi、使用蜂窝网络，打开 **https://erp.beiaiying.com:8443/**，输入设备 ID `mac1` 和 VPS 的浏览器密钥。先确认视频和点击，再验证断开释放、第二个控制者拒绝与重连后的 IDR。

证书续期后复制更新并重启本项目服务：

```sh
sudo install -o root -g screen-relay -m 640 \
  /usr/local/nginx/conf/ssl/erp.beiaiying.com_bundle.crt /etc/screen-relay/fullchain.pem
sudo install -o root -g screen-relay -m 640 \
  /usr/local/nginx/conf/ssl/erp.beiaiying.com.key /etc/screen-relay/privkey.pem
sudo systemctl restart screen-relay
```

## systemd 219 服务模板

安装包使用此模板，监听非特权端口 8443，省去较新 systemd 才支持的 `AmbientCapabilities`。独立用户使用 `/sbin/nologin`。

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
ExecStart=/opt/screen-relay/screen-relay -mode relay -listen :8443
Restart=on-failure
RestartSec=5
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

Relay 配置中的 `RELAY_PUBLIC_URL` 必须为 `https://erp.beiaiying.com:8443`，设备 ID 与 Mac Agent 一致，使用两套新生成且不同的认证密钥。TLS 私钥内容和认证密钥都不需要发到聊天中。

当前 Linux 构建产物为 `.cache/screen-relay-linux`，安装包内同样使用 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 构建，是静态链接的 x86-64 ELF。已通过本机编译、Go 测试、脚本语法检查，以及检查器对正确证书、错误域名、错误私钥的测试。本次尚未实际上传文件、安装服务或改动 VPS 防火墙，真实部署结果待用户执行上述命令反馈。

CentOS Linux 7 已于 2024-06-30 结束维护；正式长期承载公网远程控制前应安排迁移到受支持的系统。本次不升级现有 VPS，避免影响已有业务。[CentOS 官方说明](https://www.centos.org/centos-linux/)
