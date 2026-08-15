package analytics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// The whole point of the feature: an event a node's gateway buffered locally must
// come out of the platform stream shaped exactly as the consumer expects.
func TestIngestedEventSurvivesRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ing := NewIngester(rdb, ownedRoutes{"mb-ws3-shop": true}, "goma:analytics")

	sent := Event{
		Ts: time.Now().UnixMilli(), Gateway: "edge-1", Route: "mb-ws3-shop",
		Host: "shop.example.com", Method: "GET", Status: 200, Path: "/cart",
		DurationMs: 42, UpstreamMs: 30, VID: "v1", Country: "BE", UA: "Mozilla/5.0",
	}
	if _, err := ing.Ingest(context.Background(), 9, Batch{BatchID: "b", Events: []Event{sent}}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	msgs, _ := rdb.XRange(context.Background(), "goma:analytics", "-", "+").Result()
	if len(msgs) != 1 {
		t.Fatalf("stream holds %d entries", len(msgs))
	}
	var got Event
	if err := json.Unmarshal([]byte(msgs[0].Values[StreamEventField].(string)), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != sent {
		t.Fatalf("round trip changed the event:\n got %+v\nwant %+v", got, sent)
	}
	// And the consumer can still resolve its workspace + count it.
	if ws := WorkspaceIDFromRoute(got.Route); ws != 3 {
		t.Errorf("workspace = %d, want 3", ws)
	}
	agg := NewAggregator()
	agg.Ingest(&got, 3, 0)
	if n := len(agg.Flush(time.Now().Add(5 * time.Minute))); n != 1 {
		t.Errorf("aggregator produced %d buckets, want 1", n)
	}
}
