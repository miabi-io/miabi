// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package leader

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

const (
	testTTL   = 300 * time.Millisecond
	testLease = "miabi:leader:control-plane"
)

func newTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

func startTestElector(t *testing.T, rdb *redis.Client) (*Elector, func()) {
	t.Helper()
	e := newElector(rdb, "control-plane", testTTL)
	stop := e.Start(context.Background())
	t.Cleanup(stop)
	return e, stop
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOnlyOneProcessLeads(t *testing.T) {
	rdb, _ := newTestRedis(t)
	a, _ := startTestElector(t, rdb)
	if !a.Leading() {
		t.Fatal("a lone process does not lead once Start returns")
	}
	b, _ := startTestElector(t, rdb)

	time.Sleep(testTTL)
	if !a.Leading() || b.Leading() {
		t.Fatalf("leading: a=%v b=%v; want only a", a.Leading(), b.Leading())
	}
}

// miniredis never expires keys on its own, so the takeover can only come from the release.
func TestStandbyTakesOverWhenTheLeaderStops(t *testing.T) {
	rdb, _ := newTestRedis(t)
	a, stopA := startTestElector(t, rdb)
	b, _ := startTestElector(t, rdb)

	stopA()
	if a.Leading() {
		t.Fatal("a stopped process still leads")
	}
	eventually(t, "the standby leads", b.Leading)
}

func TestLeaderStepsDownWhenAnotherProcessHoldsTheLease(t *testing.T) {
	rdb, mr := newTestRedis(t)
	a, _ := startTestElector(t, rdb)

	if err := mr.Set(testLease, "another-process"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the leader steps down", func() bool { return !a.Leading() })
}

// A leader cut off from Redis must stop before its key can expire and a standby win it, or both
// would lead at once.
func TestLeaderCutOffFromRedisStepsDownBeforeItsLeaseExpires(t *testing.T) {
	rdb, mr := newTestRedis(t)
	a, _ := startTestElector(t, rdb)
	if a.validity >= testTTL {
		t.Fatalf("validity %v is not shorter than the lease TTL %v", a.validity, testTTL)
	}

	mr.SetError("connection lost")
	time.Sleep(a.validity + 20*time.Millisecond)
	if a.Leading() {
		t.Fatal("a leader that cannot renew its lease still leads after its validity ran out")
	}
}

func TestWhileRunsOnlyWhileLeading(t *testing.T) {
	rdb, mr := newTestRedis(t)
	_, stopA := startTestElector(t, rdb)
	b, _ := startTestElector(t, rdb)

	var runs, active atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.While(ctx, func(ctx context.Context) {
			runs.Add(1)
			active.Add(1)
			<-ctx.Done()
			active.Add(-1)
		})
	}()

	time.Sleep(testTTL)
	if runs.Load() != 0 {
		t.Fatal("ran on a standby")
	}

	stopA()
	eventually(t, "it runs once the standby leads", func() bool { return active.Load() == 1 })

	if err := mr.Set(testLease, "another-process"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "it stops when the lease is lost", func() bool { return active.Load() == 0 })

	mr.Del(testLease)
	eventually(t, "it runs again when the lease is won back", func() bool { return runs.Load() == 2 && active.Load() == 1 })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("While did not return after its context was cancelled")
	}
	if active.Load() != 0 {
		t.Fatal("While returned before fn did")
	}
}

func TestLeaseAcquireRenewsItsOwnKeyButNotAnothers(t *testing.T) {
	rdb, mr := newTestRedis(t)
	ctx := context.Background()
	mine := NewLease(rdb, "k", time.Minute)
	theirs := NewLease(rdb, "k", time.Minute)

	if ok, err := mine.Acquire(ctx); err != nil || !ok {
		t.Fatalf("acquire of a free key = %v, %v; want held", ok, err)
	}
	if ok, err := theirs.Acquire(ctx); err != nil || ok {
		t.Fatalf("acquire of a held key = %v, %v; want refused", ok, err)
	}

	mr.SetTTL("k", time.Second)
	if ok, err := mine.Acquire(ctx); err != nil || !ok {
		t.Fatalf("re-acquire by the holder = %v, %v; want held", ok, err)
	}
	if got := mr.TTL("k"); got != time.Minute {
		t.Fatalf("TTL = %v; the holder's re-acquire did not renew it", got)
	}

	if err := theirs.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if !mr.Exists("k") {
		t.Fatal("a release by a non-holder deleted the key")
	}
}
