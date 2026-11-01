package main

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

const loginWindow = time.Minute
const loginSourceFailures = 5
const loginGlobalFailures = 100
const loginSourceCapacity = 64

type loginFailureBucket struct {
	count int
	until time.Time
}

type loginLimiter struct {
	mu        sync.Mutex
	global    loginFailureBucket
	sources   map[string]loginFailureBucket
	nextSweep time.Time
}

// blockedLocked 在锁内检查固定失败窗口，惰性清理过期记录；容量满时不淘汰仍有效的限制。
func (l *loginLimiter) blockedLocked(source string, now time.Time) time.Duration {
	if !now.Before(l.global.until) {
		l.global = loginFailureBucket{}
	}
	var wait time.Duration
	if l.global.count >= loginGlobalFailures {
		wait = l.global.until.Sub(now)
	}
	if !now.Before(l.nextSweep) {
		for key, bucket := range l.sources {
			if !now.Before(bucket.until) {
				delete(l.sources, key)
			}
		}
		l.nextSweep = now.Add(loginWindow)
	}
	bucket, found := l.sources[source]
	if found && !now.Before(bucket.until) {
		delete(l.sources, source)
		found = false
		bucket = loginFailureBucket{}
	}
	if bucket.count >= loginSourceFailures && bucket.until.Sub(now) > wait {
		wait = bucket.until.Sub(now)
	}
	if wait > 0 {
		return wait
	}
	if !found && len(l.sources) >= loginSourceCapacity {
		// 新来源不能挤掉旧来源以重置预算；返回最早释放记录的时间，且内存最多保留64个来源。
		earliest := now.Add(loginWindow)
		for key, item := range l.sources {
			if !now.Before(item.until) {
				delete(l.sources, key)
				continue
			}
			if item.until.Before(earliest) {
				earliest = item.until
			}
		}
		if len(l.sources) >= loginSourceCapacity {
			return earliest.Sub(now)
		}
	}
	return 0
}

// blocked 在读表单前快速拒绝已受限来源，返回还需等待的时间；不分配新来源记录。
func (l *loginLimiter) blocked(source string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.blockedLocked(source, now)
}

// verify 在锁内重新检查预算并做轻量凭据比较，防止并发请求越过上限；matches不得包含I/O。
func (l *loginLimiter) verify(source string, now time.Time, matches func() bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if wait := l.blockedLocked(source, now); wait > 0 {
		return false, wait
	}
	if matches() {
		return true, 0
	}
	if l.sources == nil {
		l.sources = make(map[string]loginFailureBucket)
	}
	bucket := l.sources[source]
	if bucket.count == 0 {
		bucket.until = now.Add(loginWindow)
	}
	bucket.count++
	l.sources[source] = bucket
	if l.global.count == 0 {
		l.global.until = now.Add(loginWindow)
	}
	l.global.count++
	return false, 0
}

// loginRetry 用429及整数秒Retry-After告知等待时间，不返回密钥、失败次数或具体认证差异。
func loginRetry(w http.ResponseWriter, wait time.Duration) {
	seconds := int64((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	retry := strconv.FormatInt(seconds, 10)
	w.Header().Set("Retry-After", retry)
	http.Error(w, "登录尝试过于频繁，请在"+retry+"秒后重试", http.StatusTooManyRequests)
}
