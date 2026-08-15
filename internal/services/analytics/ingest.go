// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Ingest limits. MaxBatch bounds one POST; MaxSkew bounds how far a node's clock
// may be from the manager's before its timestamps are distrusted.
const (
	MaxBatch    = 500
	MaxSkew     = 15 * time.Minute
	batchTTL    = 10 * time.Minute
	maxStreamID = 1_000_000
)

// StreamEventField is the stream entry field Goma writes each event under, and
// the only one the consumer reads. Anything appended under another name is
// dropped without a trace, so writers and the reader share this constant.
const StreamEventField = "e"

var (
	ErrBatchTooLarge = errors.New("batch exceeds the maximum size")
	ErrNoEvents      = errors.New("batch declares no events")
	ErrNoBatchID     = errors.New("batch id is required")
)

// Batch is one node's forwarded run of gateway events.
type Batch struct {
	BatchID string  `json:"batch_id"`
	Events  []Event `json:"events"`
}

// IngestResult reports what the manager did with a batch. Rejected events are
// counted rather than failing the batch: one bad event must not strand the rest.
type IngestResult struct {
	Accepted  int  `json:"accepted"`
	Duplicate bool `json:"duplicate"`
	// Rejected counts events dropped for a route the node does not serve.
	Rejected int `json:"rejected"`
	// Reclocked counts events whose timestamp was too far off to trust.
	Reclocked int `json:"reclocked"`
}

// RouteOwnership answers which Goma route names a node is allowed to report on.
type RouteOwnership interface {
	RouteNamesForNode(serverID uint) (map[string]bool, error)
}

// Ingester appends events forwarded by an edge node to the platform stream the
// AnalyticsConsumer reads, so edge traffic converges with the manager's.
type Ingester struct {
	rdb    *redis.Client
	owner  RouteOwnership
	stream string
	maxLen int64
	now    func() time.Time
}

func NewIngester(rdb *redis.Client, owner RouteOwnership, stream string) *Ingester {
	return &Ingester{rdb: rdb, owner: owner, stream: stream, maxLen: maxStreamID, now: time.Now}
}

// acceptBatch claims a batch id and appends its events in one step. Recording the
// id after the append would double-count a retry that crashed in between, and the
// aggregator sums counters, so a replay is invisible in the dashboards.
var acceptBatch = redis.NewScript(`
local claimed = redis.call('SET', KEYS[1], '1', 'NX', 'EX', ARGV[1])
if not claimed then return 0 end
for i = 4, #ARGV do
  redis.call('XADD', KEYS[2], 'MAXLEN', '~', ARGV[2], '*', ARGV[3], ARGV[i])
end
return 1
`)

// Ingest validates a node's batch and appends the events it is allowed to report.
// Duplicate batches return the already-accepted result so the forwarder can ACK.
func (i *Ingester) Ingest(ctx context.Context, serverID uint, b Batch) (IngestResult, error) {
	switch {
	case b.BatchID == "":
		return IngestResult{}, ErrNoBatchID
	case len(b.Events) == 0:
		return IngestResult{}, ErrNoEvents
	case len(b.Events) > MaxBatch:
		return IngestResult{}, fmt.Errorf("%w: %d > %d", ErrBatchTooLarge, len(b.Events), MaxBatch)
	}

	owned, err := i.owner.RouteNamesForNode(serverID)
	if err != nil {
		return IngestResult{}, fmt.Errorf("resolve node routes: %w", err)
	}

	res := IngestResult{}
	payloads := make([]any, 0, len(b.Events)+2)
	payloads = append(payloads, int(batchTTL.Seconds()), i.maxLen, StreamEventField)
	now := i.now().UTC()
	for _, e := range b.Events {
		if !owned[e.Route] {
			res.Rejected++
			continue
		}
		if skewed(e.Ts, now) {
			e.Ts = now.UnixMilli()
			res.Reclocked++
		}
		raw, merr := json.Marshal(e)
		if merr != nil {
			res.Rejected++
			continue
		}
		payloads = append(payloads, string(raw))
		res.Accepted++
	}
	if res.Accepted == 0 {
		return res, nil
	}

	claimed, err := acceptBatch.Run(ctx, i.rdb, []string{batchKey(serverID, b.BatchID), i.stream}, payloads...).Int()
	if err != nil {
		return IngestResult{}, fmt.Errorf("append events: %w", err)
	}
	if claimed == 0 {
		return IngestResult{Duplicate: true}, nil
	}
	return res, nil
}

// skewed reports whether an event's timestamp is too far from the manager's clock
// to file into a minute bucket. Missing timestamps are handled by the aggregator.
func skewed(ts int64, now time.Time) bool {
	if ts == 0 {
		return false
	}
	d := now.Sub(time.UnixMilli(ts))
	return d > MaxSkew || d < -MaxSkew
}

func batchKey(serverID uint, id string) string {
	return fmt.Sprintf("miabi:analytics:batch:%d:%s", serverID, id)
}
