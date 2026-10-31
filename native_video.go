package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const maxNativePacket = (16 << 20) + 17

// encodeMFVideo 从Agent自身目录启动配套系统软件H264程序，参数沿用现有视频设置，不搜索PATH或启动FFmpeg。
func encodeMFVideo(ctx context.Context, send func(byte, []byte) error) error {
	if *device != "desktop" || *testPattern {
		return fmt.Errorf("MF 仅支持真实主屏 -device desktop，不支持 -test-pattern")
	}
	if *width < 2 || *width > 3840 || *width%2 != 0 || *height < 2 || *height > 2160 || *height%2 != 0 || *fps < 1 || *fps > 60 || *bitrate < 10000 || *bitrate > 20000000 {
		return fmt.Errorf("MF 输出需偶数尺寸≤3840×2160，FPS 1–60，码率10000–20000000")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	helper := filepath.Join(filepath.Dir(self), "screen_capture.exe")
	if _, err := os.Stat(helper); err != nil {
		return fmt.Errorf("需要把配套 screen_capture.exe 与 Agent 放在同一目录: %w", err)
	}
	args := []string{"--stream", "--encoder", "software", "--width", strconv.Itoa(*width), "--height", strconv.Itoa(*height), "--fps", strconv.Itoa(*fps), "--bitrate", strconv.Itoa(*bitrate)}
	return runNativeVideo(ctx, helper, args, send)
}

// runNativeVideo 管理唯一编码子进程；断连、非法数据或发送失败即终止并回收，不自动重启编码器。
func runNativeVideo(parent context.Context, helper string, args []string, send func(byte, []byte) error) error {
	ctx, stop := context.WithCancel(parent)
	defer stop()
	cmd := exec.CommandContext(ctx, helper, args...)
	cmd.Stderr = os.Stderr
	cmd.WaitDelay = 3 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动系统 H264 编码进程: %w", err)
	}
	err = forwardNativeVideo(stdout, send)
	stop()
	waitErr := cmd.Wait()
	if parent.Err() != nil {
		return parent.Err()
	}
	if err != io.EOF {
		return err
	}
	return fmt.Errorf("系统 H264 编码进程意外结束: %v（详细原因见原生日志）", waitErr)
}

// forwardNativeVideo 按显式长度读取完整原协议包，校验AUD、IDR、参数集和PTS后发送；EOF始终交给进程管理处理。
func forwardNativeVideo(r io.Reader, send func(byte, []byte) error) error {
	var previous uint64
	var frames, payload int64
	var sps, pps []byte
	first := true
	last := time.Now()
	for {
		var length [4]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(length[:])
		if n <= 17 || n > maxNativePacket {
			return fmt.Errorf("原生视频消息长度无效: %d", n)
		}
		packet := make([]byte, int(n))
		if _, err := io.ReadFull(r, packet); err != nil {
			return err
		}
		pts := binary.BigEndian.Uint64(packet[1:9])
		if packet[0] > 1 || pts > (1<<53)-1 || (!first && pts <= previous) || binary.BigEndian.Uint64(packet[9:17]) != 0 {
			return fmt.Errorf("原生视频标志或时间戳无效")
		}
		// stdout可能把任意位置拆开；只检查完整包里的NAL，不能用Read次数推断帧边界。
		scanner := bufio.NewScanner(bytes.NewReader(packet[17:]))
		scanner.Buffer(make([]byte, 65536), maxNativePacket)
		scanner.Split(splitNAL)
		aud, slices := 0, 0
		hasSPS, hasPPS := false, false
		for scanner.Scan() {
			nal := scanner.Bytes()
			if len(nal) == 0 || nal[0]&0x80 != 0 {
				return fmt.Errorf("原生 H264 NAL 无效")
			}
			typeID := nal[0] & 31
			if aud == 0 && typeID != 9 {
				return fmt.Errorf("原生访问单元必须以 AUD 开始")
			}
			switch typeID {
			case 9:
				aud++
			case 7:
				hasSPS = true
			case 8:
				hasPPS = true
			case 1, 5:
				slices++
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		key, codec := frameInfo(packet[17:], &sps, &pps)
		if aud != 1 || slices == 0 || codec == "" || len(pps) == 0 || key != (packet[0] == 1) || (first && !key) || (key && (!hasSPS || !hasPPS)) {
			return fmt.Errorf("原生视频帧边界、IDR 或 SPS/PPS 无效")
		}
		if key {
			config, _ := json.Marshal(map[string]any{"codec": codec, "width": *width, "height": *height, "fps": *fps})
			if err := send(1, config); err != nil {
				return fmt.Errorf("发送原生视频配置失败: %w", err)
			}
		}
		binary.BigEndian.PutUint64(packet[9:17], uint64(time.Now().UnixMicro()))
		if err := send(2, packet); err != nil {
			return fmt.Errorf("发送原生视频帧失败: %w", err)
		}
		previous = pts
		first = false
		frames++
		payload += int64(len(packet) - 17)
		if elapsed := time.Since(last).Seconds(); elapsed >= 1 {
			log.Printf("MF 输出 %.1f FPS / H.264 %.3f Mbps", float64(frames)/elapsed, float64(payload)*8/elapsed/1e6)
			frames, payload, last = 0, 0, time.Now()
		}
	}
}
