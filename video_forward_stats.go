package main

import (
	"log"
	"sort"
	"time"
)

type videoForwardStats struct {
	since         time.Time
	frames, bytes int
	samples       []float64
	maximum       float64
	truncated     bool
}

// record 统计Relay同步视频写入的等待和吞吐，每秒输出一次；内核接受字节不代表浏览器已显示，样本最多120条。
func (s *videoForwardStats) record(start time.Time, size int, writeErr error) {
	now := time.Now()
	if s.since.IsZero() {
		s.since = start
	}
	elapsed := now.Sub(start).Seconds() * 1000
	if len(s.samples) < 120 {
		s.samples = append(s.samples, elapsed)
	} else {
		s.truncated = true
	}
	if elapsed > s.maximum {
		s.maximum = elapsed
	}
	if writeErr == nil {
		s.frames++
		s.bytes += size
	}
	seconds := now.Sub(s.since).Seconds()
	if seconds < 1 {
		return
	}
	sort.Float64s(s.samples)
	n := len(s.samples)
	log.Printf("Relay视频转发 %.1f FPS / H.264 %.3f Mbps；WS写入 P50/P95/max=%.2f/%.2f/%.2f ms n=%d truncated=%v（含写锁等待，非端到端延迟）", float64(s.frames)/seconds, float64(s.bytes)*8/seconds/1e6, s.samples[(n+1)/2-1], s.samples[(n*95+99)/100-1], s.maximum, n, s.truncated)
	s.since = now
	s.frames = 0
	s.bytes = 0
	s.samples = s.samples[:0]
	s.maximum = 0
	s.truncated = false
}
