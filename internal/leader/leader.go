// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package leader elects the one process that runs the control plane's singleton work: scheduled
// jobs, periodic scans and cluster reconciliation. It makes a second control plane started by
// mistake stand by instead of doing that work twice. It is not multi-replica HA: agent and runner
// tunnels still belong to the process they dialled.
package leader

import (
	"context"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/metrics"
	"github.com/redis/go-redis/v9"
)

const defaultTTL = 30 * time.Second

// Elector campaigns for a named lease shared by every process on the same Redis.
type Elector struct {
	name     string
	lease    *Lease
	interval time.Duration
	validity time.Duration

	mu         sync.Mutex
	validUntil time.Time
	leading    bool
	changed    chan struct{} // closed and replaced whenever leading changes
}

// New builds an elector for the named lease. It does not campaign until Start.
func New(rdb redis.Cmdable, name string) *Elector {
	return newElector(rdb, name, defaultTTL)
}

func newElector(rdb redis.Cmdable, name string, ttl time.Duration) *Elector {
	return &Elector{
		name:  name,
		lease: NewLease(rdb, "miabi:leader:"+name, ttl),
		// Renewing every third of the TTL and trusting a renewal for half of it survives one failed
		// renewal, yet stops this process leading well before the key can expire for a standby.
		interval: ttl / 3,
		validity: ttl / 2,
		changed:  make(chan struct{}),
	}
}

// Leading reports whether this process holds the lease, confirmed recently enough that no other
// process can hold it yet.
func (e *Elector) Leading() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Now().Before(e.validUntil)
}

// Start campaigns once before returning, so a lone control plane leads from the start, then keeps
// campaigning in the background. stop ends the campaign and releases the lease, so a standby takes
// over at once instead of when the lease expires.
func (e *Elector) Start(ctx context.Context) (stop func()) {
	e.campaign(ctx)
	if !e.Leading() {
		logger.Info("leader: standing by; another process may hold the lease", "lease", e.name)
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(e.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.campaign(ctx)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
			e.resign()
		})
	}
}

// While runs fn for as long as this process leads, cancels its context when leadership is lost,
// and runs it again after each later win. It returns once ctx is done and fn has returned.
func (e *Elector) While(ctx context.Context, fn func(context.Context)) {
	for {
		leading, changed := e.state()
		if !leading {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				continue
			}
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			fn(runCtx)
		}()
	held:
		for {
			select {
			case <-ctx.Done():
				break held
			case <-changed:
				if leading, changed = e.state(); !leading {
					break held
				}
			}
		}
		cancel()
		<-done
		if ctx.Err() != nil {
			return
		}
	}
}

func (e *Elector) state() (bool, <-chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leading, e.changed
}

func (e *Elector) campaign(ctx context.Context) {
	start := time.Now()
	actx, cancel := context.WithTimeout(ctx, e.interval/2)
	ok, err := e.lease.Acquire(actx)
	cancel()

	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case err != nil:
		// An unreachable Redis is not a lost lease: a held one stays trusted until its validity runs out.
		if ctx.Err() == nil {
			logger.Warn("leader: lease campaign failed", "lease", e.name, "error", err)
		}
	case ok:
		e.validUntil = start.Add(e.validity)
	default:
		e.validUntil = time.Time{}
	}
	leading := time.Now().Before(e.validUntil)
	if !e.setLeadingLocked(leading) {
		return
	}
	if leading {
		logger.Info("leader: this process now leads", "lease", e.name)
	} else {
		logger.Warn("leader: this process no longer leads", "lease", e.name)
	}
}

func (e *Elector) resign() {
	e.mu.Lock()
	e.validUntil = time.Time{}
	e.setLeadingLocked(false)
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), e.interval)
	defer cancel()
	if err := e.lease.Release(ctx); err != nil {
		logger.Warn("leader: lease release failed; a standby takes over once it expires", "lease", e.name, "error", err)
	}
}

func (e *Elector) setLeadingLocked(leading bool) (changed bool) {
	metrics.SetLeader(e.name, leading)
	if leading == e.leading {
		return false
	}
	e.leading = leading
	close(e.changed)
	e.changed = make(chan struct{})
	return true
}

// Never never leads. A process that must leave singleton work to the control plane passes it
// wherever a leader is expected.
var Never never

type never struct{}

func (never) Leading() bool { return false }
