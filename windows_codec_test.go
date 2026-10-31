package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type byteReader struct{ r *bytes.Reader }

// Read 强制每次只交付一个字节，验证分帧不依赖 stdout 某次 Read 的大小。
func (r byteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.r.Read(p)
}

// TestWindowsCodecFixture 用真实 libx264 生成带黑边的固定测试流，并验证逐字节 AU 切分、IDR、参数集和软件解码。
func TestWindowsCodecFixture(t *testing.T) {
	l, err := fitWindowsVideo(desktopRect{Width: 1280, Height: 1024}, desktopRect{Width: 1280, Height: 1024}, 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	_, filter := windowsCaptureArgs(l, 20)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x1024:rate=20", "-frames:v", "40", "-an", "-vf", filter, "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-tune", "zerolatency", "-b:v", "2000000", "-bf", "0", "-g", "20", "-x264-params", "scenecut=0", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
	var errors bytes.Buffer
	cmd.Stderr = &errors
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("固定流编码失败: %v %s", err, errors.String())
	}
	var frames [][]byte
	// 先按正常读取方式保存完整访问单元。
	if err := readFrames(bytes.NewReader(raw), func(b []byte) error { frames = append(frames, bytes.Clone(b)); return nil }); err != nil {
		t.Fatal(err)
	}
	var fragmented [][]byte
	// 再逐字节读取，切分结果必须完全相同。
	if err := readFrames(byteReader{bytes.NewReader(raw)}, func(b []byte) error { fragmented = append(fragmented, bytes.Clone(b)); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 40 || !reflect.DeepEqual(frames, fragmented) {
		t.Fatalf("完整帧=%d fragmented=%d", len(frames), len(fragmented))
	}
	var sps, pps []byte
	var keys []int
	var codec string
	var packets []string
	for i, frame := range frames {
		key, name := frameInfo(frame, &sps, &pps)
		if key {
			keys = append(keys, i)
			codec = name
			if len(pps) == 0 {
				t.Fatal("IDR 缺少 PPS")
			}
		}
		packet := make([]byte, 17+len(frame))
		if key {
			packet[0] = 1
		}
		binary.BigEndian.PutUint64(packet[1:9], uint64(i)*50000)
		// 固定流不伪造采集时间；浏览器验证只评估解码与恢复，不报告端到端延迟。
		copy(packet[17:], frame)
		packets = append(packets, base64.StdEncoding.EncodeToString(packet))
	}
	if !reflect.DeepEqual(keys, []int{0, 20}) {
		t.Fatalf("关键帧索引=%v", keys)
	}
	decode := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-xerror", "-f", "h264", "-i", "pipe:0", "-f", "null", "-")
	decode.Stdin = bytes.NewReader(raw)
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("固定流软件解码失败: %v %s", err, output)
	}
	folder := filepath.Join(".cache", "windows-validation")
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(map[string]any{"codec": codec, "width": 1920, "height": 1080, "fps": 20, "packets": packets})
	if err := os.WriteFile(filepath.Join(folder, "fixture.json"), fixture, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "fixed.h264"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	harness, err := os.ReadFile(filepath.Join("windows", "fixture-test.js"))
	if err != nil {
		t.Fatal(err)
	}
	page := strings.Replace(string(html), "</html>", "<script>\nconst fixedStream="+string(fixture)+";\n"+string(harness)+"\n</script></html>", 1)
	if err := os.WriteFile(filepath.Join(folder, "fixed-h264.html"), []byte(page), 0644); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if err := os.WriteFile(filepath.Join(folder, "fixed.sha256"), []byte(hex.EncodeToString(hash[:])+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("固定流: codec=%s AU=%d IDR=%v bytes=%d SHA256=%x；软件解码通过，WebCodecs 待实际浏览器验证", codec, len(frames), keys, len(raw), hash)
}
