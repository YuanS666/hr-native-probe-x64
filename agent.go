package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

type mouseController interface {
	handle(mouseMessage) error
	release() error
}

// openAgentMouse 按现有开关和系统权限创建控制器，不可用时仍允许远程观看视频。
func openAgentMouse() (mouseController, error) {
	if !*mouseEnabled && !*keyboardEnabled {
		return nil, fmt.Errorf("未启用输入；Agent 使用 -mouse 和/或 -keyboard 显式授权")
	}
	m, err := openNativeInput(*device)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// sendAgentFrame 为原视频或配置加上随机会话外层，Relay 去除外层后浏览器仍收到原格式。
func sendAgentFrame(w *websocket, sid string, op byte, b []byte) error {
	session, err := hex.DecodeString(sid)
	if err != nil || len(session) != 16 {
		return fmt.Errorf("视频会话无效")
	}
	packet := make([]byte, 17+len(b))
	copy(packet, session)
	packet[16] = op
	copy(packet[17:], b)
	return w.write(2, packet)
}

// agentConnection 在一个认证连接内依次处理观看会话；注入编码和鼠标工厂以便无屏幕环境测试清理行为。
func agentConnection(parent context.Context, w *websocket, encode func(context.Context, func(byte, []byte) error) error, openMouse func() (mouseController, error)) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer w.Close()
	go w.keepAlive(ctx)
	type incoming struct {
		op  byte
		b   []byte
		err error
	}
	input := make(chan incoming, 1)
	// 唯一读取协程将连接消息交给会话循环；取消时不会遗留阻塞的发送协程。
	go func() {
		for {
			op, b, err := w.read()
			select {
			case input <- incoming{op, b, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var session string
	var mouse mouseController
	var stopEncode context.CancelFunc
	var encoded chan error
	var ready chan struct{}
	var retryTimer *time.Timer
	var retryTick <-chan time.Time
	var retries int
	var runningSince time.Time
	paused := false
	nativeMode := *encoder == "mf"
	// 采集状态沿用认证会话封装，Relay只转发，不增加第二套通道。
	captureStatus := func(state, message string) error {
		log.Printf("采集状态 %s: %s", state, message)
		b, _ := json.Marshal(map[string]string{"type": "capture-status", "state": state, "message": message})
		return sendAgentFrame(w, session, 1, b)
	}
	// 每次恢复都重新查询普通桌面权限和显示布局，不复用旧鼠标坐标映射。
	reportMouse := func() error {
		var err error
		mouse, err = openMouse()
		status := "原生输入权限可用，可按需勾选鼠标/键盘"
		if err != nil {
			status = err.Error()
			mouse = nil
		}
		b, _ := json.Marshal(map[string]any{"type": "mouse-status", "enabled": mouse != nil && *mouseEnabled, "keyboard": mouse != nil && *keyboardEnabled, "message": status})
		return sendAgentFrame(w, session, 1, b)
	}
	// 上个进程已回收后才启动新编码器；新首帧必须经既有校验确认IDR和参数集。
	launchEncode := func() {
		videoCtx, stop := context.WithCancel(ctx)
		stopEncode = stop
		done := make(chan error, 1)
		encoded = done
		firstFrame := make(chan struct{}, 1)
		ready = firstFrame
		sid := session
		runningSince = time.Time{}
		go func() {
			first := true
			err := encode(videoCtx, func(op byte, b []byte) error {
				if videoCtx.Err() != nil {
					return videoCtx.Err()
				}
				if err := sendAgentFrame(w, sid, op, b); err != nil {
					return err
				}
				if first && op == 2 {
					first = false
					firstFrame <- struct{}{}
				}
				return nil
			})
			done <- err
		}()
	}
	// 先释放鼠标再等待编码退出，断开浏览器不会因视频写入阻塞而拖延按键清理。
	stopSession := func() {
		if retryTimer != nil {
			retryTimer.Stop()
			retryTimer = nil
		}
		retryTick = nil
		ready = nil
		retries = 0
		paused = false
		if mouse != nil {
			mouse.release()
			mouse = nil
		}
		if stopEncode != nil {
			stopEncode()
			stopEncode = nil
		}
		if encoded != nil {
			<-encoded
			encoded = nil
		}
		session = ""
	}
	defer stopSession()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-retryTick:
			retryTick = nil
			retryTimer = nil
			if err := captureStatus("starting", "正在重新初始化桌面采集"); err != nil {
				return err
			}
			launchEncode()
		case <-ready:
			ready = nil
			if nativeMode {
				runningSince = time.Now()
				paused = false
				if err := captureStatus("running", "已收到新IDR，采集恢复"); err != nil {
					return err
				}
				if mouse != nil {
					mouse.release()
					mouse = nil
				}
				if err := reportMouse(); err != nil {
					return err
				}
			}
		case err := <-encoded:
			encoded = nil
			ready = nil
			if stopEncode != nil {
				stopEncode()
				stopEncode = nil
			}
			var failure *nativeCaptureError
			if nativeMode && errors.As(err, &failure) {
				paused = true
				if mouse != nil {
					mouse.release()
					mouse = nil
				}
				// 稳定运行一分钟才重置预算，防止短暂恢复造成无限高速重启。
				if !runningSince.IsZero() && time.Since(runningSince) >= time.Minute {
					retries = 0
				}
				delay, allowed := captureRetryDelay(retries)
				if failure.retry && allowed {
					retries++
					if err := captureStatus("paused", fmt.Sprintf("%v；%s后完整重建，第%d/6次", failure, delay, retries)); err != nil {
						return err
					}
					retryTimer = time.NewTimer(delay)
					retryTick = retryTimer.C
				} else {
					if err := captureStatus("stopped", fmt.Sprintf("%v；自动恢复已停止，请检查原生日志后重新开始", failure)); err != nil {
						return err
					}
				}
				continue
			}
			sid := session
			stopSession()
			failureJSON, _ := json.Marshal(map[string]string{"error": fmt.Sprint("Agent 编码终止: ", err)})
			sendAgentFrame(w, sid, 1, failureJSON)
			return fmt.Errorf("编码结束: %v", err)
		case message := <-input:
			if message.err != nil {
				return message.err
			}
			cmd, err := decodeRelayCommand(message.b)
			if message.op != 1 || err != nil || cmd.Device != *deviceID {
				return fmt.Errorf("Relay 指令格式或设备无效")
			}
			switch cmd.Type {
			case "start":
				stopSession()
				session = cmd.Session
				if nativeMode {
					paused = true
					if err := captureStatus("starting", "正在初始化桌面采集"); err != nil {
						return err
					}
				} else if err := reportMouse(); err != nil {
					return err
				}
				launchEncode()
			case "stop":
				if cmd.Session != session {
					return fmt.Errorf("停止指令会话不匹配")
				}
				stopSession()
			case "control":
				if session == "" || cmd.Session != session {
					return fmt.Errorf("控制指令会话不匹配")
				}
				msg, err := parseInput(cmd.Mouse)
				if err != nil {
					return err
				}
				if nativeMode && paused {
					continue
				}
				if nativeMode && mouse == nil && msg.Action == "heartbeat" {
					if err := reportMouse(); err != nil {
						return err
					}
				}
				if mouse == nil {
					if msg.Action == "heartbeat" || msg.Action == "release" {
						continue
					}
					if nativeMode {
						continue
					}
					return fmt.Errorf("未获准执行鼠标控制")
				}
				if err := mouse.handle(msg); err != nil {
					if !nativeMode {
						return err
					}
					mouse.release()
					mouse = nil
					log.Printf("鼠标暂停（保留视频/WSS）: %v", err)
					state, _ := json.Marshal(map[string]any{"type": "mouse-status", "enabled": false, "keyboard": false, "message": err.Error()})
					if err := sendAgentFrame(w, session, 1, state); err != nil {
						return err
					}
				}
			}
		}
	}
}

// runAgent 校验证书与环境密钥后主动连接 Relay，断线使用 1–30 秒指数退避，退出时停止重试。
func runAgent(ctx context.Context) error {
	key := os.Getenv("RELAY_AGENT_KEY")
	u, err := url.Parse(*relayAddress)
	if err != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/agent" {
		return fmt.Errorf("需要 -relay wss://域名[:端口]/agent")
	}
	if !validDevice(*deviceID) || len(key) < 32 {
		return fmt.Errorf("需要设备 ID 和至少 32 字节的 RELAY_AGENT_KEY")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			return err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("CA 文件不包含有效证书")
		}
		config.RootCAs = pool
	}
	headers := http.Header{"Authorization": []string{"Bearer " + key}, "X-Device-Id": []string{*deviceID}}
	delay := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		w, err := dialWS(ctx, *relayAddress, headers, config)
		if err == nil {
			w.limit = 1024
			log.Print("Agent: 已通过 WSS 连接 Relay，等待浏览器")
			err = agentConnection(ctx, w, encodeVideo, openAgentMouse)
		}
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > 30*time.Second {
			delay = time.Second
		}
		log.Printf("Agent: 连接结束（%v），%s 后重连", err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
	return nil
}
