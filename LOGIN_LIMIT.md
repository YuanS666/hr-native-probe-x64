# Relay 登录失败限频

仅修改浏览器 POST /login，视频、鼠标、键盘及 Agent 认证协议保持不变。无新增依赖。

- 同一来源固定一分钟窗口最多执行5次失败认证；全局最多100次。达到上限后返回429及 Retry-After，冷却期间正确密钥同样不能新登录。
- 只采用实际 TCP 对端地址；忽略 Forwarded/X-Forwarded-For；IPv4映射地址统一，IPv6按/64归组。
- 最多64条来源记录，惰性清理过期项；容量满时新来源暂时被拒绝，不淘汰有效记录。正确认证不分配记录、不清除失败预算。
- 限制窗口不因受限请求延长。重启Relay清空内存计数。
- 已签发Cookie的/session、浏览器WSS及Agent连接不经过此限频。原有Cookie期限及单控制者限制不变。

局限：共享公网IP的用户共享预算；全局失败过多或来源容量耗尽可能暂时阻止合法新登录。不是全面的DDoS防护。若增加反向代理，多个用户会共享代理地址预算；当前方案不信任客户端自行提交的代理头。

## 本次部署

只需更新VPS的screen-relay，Windows/Mac及原生采集模块无需为此重建。使用现有服务配置、密钥、设备ID、证书和启动参数。不要给systemctl或Relay添加-keyboard。

先通过 `systemctl list-units --type=service --all | grep -i relay` 确认实际服务名，再用 `systemctl cat 实际服务名` 确认 ExecStart 指向：

`/home/wwwroot/erp.beiaiying.com/screen-relay-release/screen-relay`

备份旧二进制，停止实际服务，用新文件替换上述二进制，执行 `chmod +x` 后启动同一服务；用 `journalctl -u 实际服务名 -n 50 --no-pager` 查看“登录失败限频已启用”。不更改环境配置或证书。若目前手动启动，沿用之前命令，仅替换二进制。

## 人工验收

1. 正确设备ID和浏览器密钥正常登录，查看画面及控制。
2. 在同一来源浏览器退出登录后，连续提交5次错误密钥；后续提交显示频繁提示（HTTP429，包含Retry-After），此时正确密钥也受限。
3. 等待提示秒数后，用正确密钥重新登录并控制。
4. 另一已登录浏览器会话及Agent重连不应因登录限频失效；单控制浏览器限制依旧存在。
5. 不要在日志或截图中泄露实际密钥。

自动测试使用内存TLS/WebSocket连接及虚拟时间，不监听端口；通过不代表VPS公网部署已验证。
