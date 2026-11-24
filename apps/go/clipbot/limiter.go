package main

import (
	"sync"
	"time"
)

// limiter enforces one global and one per-viewer cooldown. State is in memory,
// which is correct only while a single replica runs.
type limiter struct {
	mu         sync.Mutex
	now        func() time.Time
	global     time.Duration
	perUser    time.Duration
	lastGlobal time.Time
	lastUser   map[string]time.Time
}

func newLimiter(global, perUser time.Duration) *limiter {
	return &limiter{now: time.Now, global: global, perUser: perUser, lastUser: map[string]time.Time{}}
}

// allow reports whether user may clip now. On refusal it returns the wait.
// A refused call never extends a cooldown.
func (l *limiter) allow(user string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if wait := l.global - now.Sub(l.lastGlobal); wait > 0 {
		return false, wait
	}
	if last, ok := l.lastUser[user]; ok {
		if wait := l.perUser - now.Sub(last); wait > 0 {
			return false, wait
		}
	}
	l.lastGlobal = now
	l.lastUser[user] = now
	l.prune(now)
	return true, 0
}

// prune bounds memory: viewer names are attacker-influenced input.
func (l *limiter) prune(now time.Time) {
	if len(l.lastUser) < 1024 {
		return
	}
	for u, t := range l.lastUser {
		if now.Sub(t) > l.perUser {
			delete(l.lastUser, u)
		}
	}
}
