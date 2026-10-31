package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"time"
	"unicode/utf8"
)

type mouseMessage struct {
	Type   string   `json:"type"`
	Action string   `json:"action"`
	X      *float64 `json:"x,omitempty"`
	Y      *float64 `json:"y,omitempty"`
	Button *int     `json:"button,omitempty"`
	Clicks *int     `json:"clicks,omitempty"`
	Delta  *int     `json:"delta,omitempty"`
}

// parseMouse 严格解析至多 512 字节的 JSON，拒绝未知字段、缺失坐标和不合法的动作参数。
func parseMouse(b []byte) (mouseMessage, error) {
	var msg mouseMessage
	if len(b) > 512 || !utf8.Valid(b) {
		return msg, fmt.Errorf("控制消息过长或不是 UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&msg); err != nil {
		return msg, err
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return msg, fmt.Errorf("控制消息包含多余数据")
	}
	if msg.Type != "mouse" {
		return msg, fmt.Errorf("未知消息类型")
	}
	switch msg.Action {
	case "release", "heartbeat":
		if msg.X != nil || msg.Y != nil || msg.Button != nil || msg.Clicks != nil || msg.Delta != nil {
			return msg, fmt.Errorf("清理/保活消息不能附带鼠标参数")
		}
		return msg, nil
	case "move", "down", "up", "wheel":
	default:
		return msg, fmt.Errorf("未知鼠标动作")
	}
	if msg.X == nil || msg.Y == nil || !(*msg.X >= 0 && *msg.X <= 1 && *msg.Y >= 0 && *msg.Y <= 1) {
		return msg, fmt.Errorf("坐标必须在 0–1 范围")
	}
	if msg.Action == "down" || msg.Action == "up" {
		if msg.Button == nil || *msg.Button < 0 || *msg.Button > 1 || msg.Delta != nil {
			return msg, fmt.Errorf("仅支持左右鼠标键")
		}
		if msg.Action == "down" && (msg.Clicks == nil || *msg.Clicks < 1 || *msg.Clicks > 2) {
			return msg, fmt.Errorf("点击次数只能为 1 或 2")
		}
		if msg.Action == "up" && msg.Clicks != nil {
			return msg, fmt.Errorf("释放消息不能指定点击次数")
		}
	} else {
		if msg.Button != nil || msg.Clicks != nil {
			return msg, fmt.Errorf("移动/滚动消息不能附带按键参数")
		}
		if msg.Action == "wheel" && (msg.Delta == nil || *msg.Delta == 0 || *msg.Delta < -600 || *msg.Delta > 600) {
			return msg, fmt.Errorf("滚动量必须为非零整数且绝对值不超过 600")
		}
		if msg.Action == "move" && msg.Delta != nil {
			return msg, fmt.Errorf("移动消息不能附带滚动量")
		}
	}
	return msg, nil
}

// readClientWS 读取单个掩码客户端帧；限制长度且拒绝扩展位、分片及非法控制帧，避免无界分配。
func readClientWS(r io.Reader) (byte, []byte, error) {
	op, b, err := readWSFrame(r, true, 512)
	if op == 2 {
		return 0, nil, fmt.Errorf("不接受客户端二进制指令")
	}
	return op, b, err
}

// readControls 从现有连接接收鼠标 JSON 或协议控制帧；所有退出路径释放会话按键，5 秒无保活则断开控制会话。
func readControls(c net.Conn, r io.Reader, mouse *nativeMouse) error {
	if mouse != nil {
		defer mouse.release()
	}
	for {
		if mouse != nil {
			c.SetReadDeadline(time.Now().Add(5 * time.Second))
		}
		opcode, b, err := readClientWS(r)
		if err != nil {
			return err
		}
		switch opcode {
		case 8:
			if mouse != nil {
				mouse.release()
			}
			return writeWS(c, 8, b)
		case 9:
			if err := writeWS(c, 10, b); err != nil {
				return err
			}
			continue
		case 10:
			continue
		}
		msg, err := parseMouse(b)
		if err != nil {
			return fmt.Errorf("非法控制消息: %w", err)
		}
		if mouse == nil {
			return fmt.Errorf("当前会话没有启用鼠标控制")
		}
		if err := mouse.handle(msg); err != nil {
			log.Printf("鼠标异常: %v", err)
			return err
		}
	}
}
