#!/bin/bash
set -euo pipefail
umask 077

if [ "$(id -u)" -ne 0 ]; then
  echo '请在 VPS 上使用 sudo bash install-centos7.sh 运行。' >&2
  exit 1
fi
if [ "$(uname -m)" != x86_64 ]; then
  echo '此安装包仅适用于 Linux x86_64。' >&2
  exit 1
fi

relay_release=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
relay_cert=/usr/local/nginx/conf/ssl/erp.beiaiying.com_bundle.crt
relay_key=/usr/local/nginx/conf/ssl/erp.beiaiying.com.key

# 在修改服务之前核对上传完整性、可执行性和 TLS 基础配置，避免证书错误后才停止旧服务。
cd "$relay_release"
sha256sum -c SHA256SUMS
./screen-relay -h >/dev/null 2>&1
./check-tls "$relay_cert" "$relay_key" erp.beiaiying.com

if ! id screen-relay >/dev/null 2>&1; then
  useradd --system --home-dir /opt/screen-relay --shell /sbin/nologin screen-relay
fi
install -d -o root -g root -m 755 /opt/screen-relay
install -d -o root -g screen-relay -m 750 /etc/screen-relay

# 已有配置保留原认证密钥，重复安装不会让 Mac 和浏览器突然失去凭证。
if [ ! -e /etc/screen-relay/relay.env ]; then
  relay_agent_key=$(openssl rand -hex 32)
  relay_viewer_key=$(openssl rand -hex 32)
  printf '%s\n' \
    'RELAY_DEVICE_ID=mac1' \
    "RELAY_AGENT_KEY=$relay_agent_key" \
    "RELAY_VIEWER_KEY=$relay_viewer_key" \
    'RELAY_PUBLIC_URL=https://erp.beiaiying.com:8443' \
    'RELAY_TLS_CERT=/etc/screen-relay/fullchain.pem' \
    'RELAY_TLS_KEY=/etc/screen-relay/privkey.pem' > /etc/screen-relay/relay.env
  unset relay_agent_key relay_viewer_key
fi
chown root:root /etc/screen-relay/relay.env
chmod 600 /etc/screen-relay/relay.env

# 只停止本项目的旧服务；证书复制到专用目录，原站点证书及 Nginx 配置均不修改。
if systemctl is-active --quiet screen-relay.service; then
  systemctl stop screen-relay.service
fi
install -o root -g root -m 755 screen-relay /opt/screen-relay/screen-relay
install -o root -g screen-relay -m 640 "$relay_cert" /etc/screen-relay/fullchain.pem
install -o root -g screen-relay -m 640 "$relay_key" /etc/screen-relay/privkey.pem
install -o root -g root -m 644 screen-relay.service /etc/systemd/system/screen-relay.service
systemctl daemon-reload
systemd-analyze verify /etc/systemd/system/screen-relay.service
systemctl enable screen-relay.service
systemctl restart screen-relay.service
systemctl status screen-relay.service --no-pager -l

echo '服务已提交启动。公网访问还需 ECS 安全组及现有主机防火墙允许 TCP 8443。'
echo '浏览器：https://erp.beiaiying.com:8443/'
echo 'Agent：wss://erp.beiaiying.com:8443/agent'
echo '认证密钥位于 /etc/screen-relay/relay.env；请仅自行复制，不发送到聊天。'
