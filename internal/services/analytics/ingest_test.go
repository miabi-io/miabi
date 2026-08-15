// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type ownedRoutes map[string]bool

func (o ownedRoutes) RouteNamesForNode(uint) (map[string]bool, error) { return o, nil }

type failingOwner struct{}

func (failingOwner) RouteNamesForNode(uint) (map[string]bool, error) {
	return nil, errors.New("boom")
}

const stream = "goma:analytics"

func newIngester(t *testing.T, owner RouteOwnership) (*Ingester, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewIngester(rdb, owner, stream), mr, rdb
}

func event(route string, ts int64) Event {
	return Event{Ts: ts, Route: route, Host: "example.com", Method: "GET", Status: 200, DurationMs: 12}
}

func streamed(t *testing.T, rdb *redis.Client) []Event {
	t.Helper()
	msgs, err := rdb.XRange(context.Background(), stream, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange: %v", err)
	}
	out := make([]Event, 0, len(msgs))
	for _, m := range msgs {
		raw, _ := m.Values[StreamEventField].(string)
		var e Event
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatalf("stored event is not an Event: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func TestIngestAppendsOwnedRoutes(t *testing.T) {
	ing, _, rdb := newIngester(t, ownedRoutes{"mb-ws1-web": true})
	now := time.Now().UnixMilli()

	res, err := ing.Ingest(context.Background(), 7, Batch{
		BatchID: "b1",
		Events:  []Event{event("mb-ws1-web", now), event("mb-ws1-web", now)},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Accepted != 2 || res.Rejected != 0 || res.Duplicate {
		t.Fatalf("result = %+v, want 2 accepted", res)
	}
	if got := streamed(t, rdb); len(got) != 2 {
		t.Fatalf("stream holds %d events, want 2", len(got))
	}
}

// The workspace is parsed straight out of the route name, so a node reporting a
// route it does not serve is claiming another tenant's traffic.
func TestIngestRejectsForeignRoutes(t *testing.T) {
	ing, _, rdb := newIngester(t, ownedRoutes{"mb-ws1-web": true})
	now := time.Now().UnixMilli()

	res, err := ing.Ingest(context.Background(), 7, Batch{
		BatchID: "b1",
		Events:  []Event{event("mb-ws1-web", now), event("mb-ws42-victim", now), event("mb-ws42-anything", now)},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Accepted != 1 || res.Rejected != 2 {
		t.Fatalf("result = %+v, want 1 accepted / 2 rejected", res)
	}
	for _, e := range streamed(t, rdb) {
		if e.Route != "mb-ws1-web" {
			t.Errorf("foreign route %q reached the stream", e.Route)
		}
	}
}

// A replayed batch must not double-count: the aggregator sums counters, so a
// duplicate would inflate the dashboards invisibly.
func TestIngestDeduplicatesBatch(t *testing.T) {
	ing, _, rdb := newIngester(t, ownedRoutes{"mb-ws1-web": true})
	b := Batch{BatchID: "same", Events: []Event{event("mb-ws1-web", time.Now().UnixMilli())}}

	if _, err := ing.Ingest(context.Background(), 7, b); err != nil {
		t.Fatalf("first: %v", err)
	}
	res, err := ing.Ingest(context.Background(), 7, b)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !res.Duplicate {
		t.Errorf("replay reported %+v, want Duplicate", res)
	}
	if got := streamed(t, rdb); len(got) != 1 {
		t.Fatalf("stream holds %d events after a replay, want 1", len(got))
	}
}

// Batch ids are per node, so two nodes using the same id do not shadow each other.
func TestIngestBatchIDIsScopedToNode(t *testing.T) {
	ing, _, rdb := newIngester(t, ownedRoutes{"mb-ws1-web": true})
	b := Batch{BatchID: "same", Events: []Event{event("mb-ws1-web", time.Now().UnixMilli())}}

	if _, err := ing.Ingest(context.Background(), 1, b); err != nil {
		t.Fatalf("node 1: %v", err)
	}
	res, err := ing.Ingest(context.Background(), 2, b)
	if err != nil {
		t.Fatalf("node 2: %v", err)
	}
	if res.Duplicate {
		t.Error("a second node's batch was treated as a replay")
	}
	if got := streamed(t, rdb); len(got) != 2 {
		t.Fatalf("stream holds %d events, want 2", len(got))
	}
}

// A node with a broken clock would file traffic in the wrong minute bucket, but
// dropping it loses real traffic — stamp arrival time and count it instead.
func TestIngestReclocksSkewedEvents(t *testing.T) {
	ing, _, rdb := newIngester(t, ownedRoutes{"mb-ws1-web": true})
	now := time.Now()
	ing.now = func() time.Time { return now }

	skewed := now.Add(-2 * time.Hour).UnixMilli()
	res, err := ing.Ingest(context.Background(), 7, Batch{
		BatchID: "b1",
		Events:  []Event{event("mb-ws1-web", skewed), event("mb-ws1-web", now.UnixMilli())},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Accepted != 2 || res.Reclocked != 1 {
		t.Fatalf("result = %+v, want 2 accepted / 1 reclocked", res)
	}
	for _, e := range streamed(t, rdb) {
		if e.Ts == skewed {
			t.Error("a skewed timestamp reached the stream unchanged")
		}
	}
}

// A missing timestamp is the aggregator's job (it buckets to now), not a skew.
func TestIngestKeepsZeroTimestamp(t *testing.T) {
	ing, _, _ := newIngester(t, ownedRoutes{"mb-ws1-web": true})
	res, err := ing.Ingest(context.Background(), 7, Batch{
		BatchID: "b1", Events: []Event{event("mb-ws1-web", 0)},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Reclocked != 0 || res.Accepted != 1 {
		t.Fatalf("result = %+v, want 1 accepted / 0 reclocked", res)
	}
}

// These are permanent: the forwarder has to drop the batch, so they must be
// distinguishable from a transient failure.
func TestIngestRejectsMalformedBatches(t *testing.T) {
	ing, _, _ := newIngester(t, ownedRoutes{"mb-ws1-web": true})
	big := make([]Event, MaxBatch+1)
	for i := range big {
		big[i] = event("mb-ws1-web", time.Now().UnixMilli())
	}
	tests := []struct {
		name string
		in   Batch
		want error
	}{
		{"no batch id", Batch{Events: []Event{event("mb-ws1-web", 0)}}, ErrNoBatchID},
		{"no events", Batch{BatchID: "b"}, ErrNoEvents},
		{"too large", Batch{BatchID: "b", Events: big}, ErrBatchTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ing.Ingest(context.Background(), 7, tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// A batch of only foreign routes must not claim its id: nothing was stored, so a
// corrected retry has to be able to land.
func TestIngestAllRejectedDoesNotClaimBatch(t *testing.T) {
	ing, _, rdb := newIngester(t, ownedRoutes{"mb-ws1-web": true})
	res, err := ing.Ingest(context.Background(), 7, Batch{
		BatchID: "b1", Events: []Event{event("mb-ws9-other", time.Now().UnixMilli())},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Accepted != 0 || res.Rejected != 1 {
		t.Fatalf("result = %+v, want 0 accepted / 1 rejected", res)
	}
	if n, _ := rdb.Exists(context.Background(), batchKey(7, "b1")).Result(); n != 0 {
		t.Error("an empty batch claimed its id, blocking a corrected retry")
	}
}

func TestIngestOwnerFailureIsTransient(t *testing.T) {
	ing, _, _ := newIngester(t, failingOwner{})
	_, err := ing.Ingest(context.Background(), 7, Batch{
		BatchID: "b1", Events: []Event{event("mb-ws1-web", 0)},
	})
	if err == nil {
		t.Fatal("expected an error when route ownership cannot be resolved")
	}
	for _, permanent := range []error{ErrNoBatchID, ErrNoEvents, ErrBatchTooLarge} {
		if errors.Is(err, permanent) {
			t.Errorf("a lookup failure reported as permanent (%v)", permanent)
		}
	}
}

// The consumer reads exactly one field name. Appending under any other drops
// every forwarded event with no error anywhere — the failure this feature exists
// to remove, reintroduced one layer down.
func TestIngestWritesTheFieldTheConsumerReads(t *testing.T) {
	ing, _, rdb := newIngester(t, ownedRoutes{"mb-ws1-web": true})
	if _, err := ing.Ingest(context.Background(), 7, Batch{
		BatchID: "b", Events: []Event{event("mb-ws1-web", time.Now().UnixMilli())},
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	msgs, err := rdb.XRange(context.Background(), stream, "-", "+").Result()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("XRange: %v (%d entries)", err, len(msgs))
	}
	if _, ok := msgs[0].Values[StreamEventField]; !ok {
		t.Fatalf("entry has fields %v, want one named %q", msgs[0].Values, StreamEventField)
	}
}
