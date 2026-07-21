// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/services/analytics"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/redis/go-redis/v9"
)

const (
	// analyticsGroup is the Redis consumer group shared by every worker, so each
	// gateway event is rolled up exactly once regardless of worker count.
	analyticsGroup = "miabi-analytics"
	// analyticsBucketGrace keeps the current (and just-past) minute open so
	// late-arriving events still land in their bucket before it is flushed.
	analyticsBucketGrace = 90 * time.Second
	// analyticsRouteTTL bounds how stale the route→app reverse map may get.
	analyticsRouteTTL = time.Minute
)

// AnalyticsConsumer reads Goma Gateway's per-request event stream, rolls events
// into minute buckets (models.AnalyticsRollup) and persists closed buckets on an
// interval. It resolves each event's workspace from the route name and its app
// from a cached route→app map. Holds no per-request rows and no PII.
type AnalyticsConsumer struct {
	rdb      *redis.Client
	routes   *repositories.RouteRepository
	store    *repositories.AnalyticsRepository
	agg      *analytics.Aggregator
	stream   string
	consumer string

	flushEvery time.Duration
	// retentionDays returns the effective number of days of rollups to keep,
	// resolved at prune time so a license install/expiry takes effect live
	// (Community clamps to enterprise.CommunityAnalyticsRetentionDays).
	retentionDays func() int

	// route→app cache
	routeMap    map[string]uint // gomaName → applicationID
	routeLoaded time.Time
}

// NewAnalyticsConsumer wires the consumer. consumer is this worker's unique name
// within the group (so pending-message ownership is per-worker). retentionDays is
// evaluated on each prune; nil disables pruning (keep forever).
func NewAnalyticsConsumer(rdb *redis.Client, routes *repositories.RouteRepository, store *repositories.AnalyticsRepository, stream, consumer string, flushEvery time.Duration, retentionDays func() int) *AnalyticsConsumer {
	if flushEvery <= 0 {
		flushEvery = 15 * time.Second
	}
	return &AnalyticsConsumer{
		rdb: rdb, routes: routes, store: store, agg: analytics.NewAggregator(),
		stream: stream, consumer: consumer,
		flushEvery: flushEvery, retentionDays: retentionDays,
		routeMap: map[string]uint{},
	}
}

// Run consumes until ctx is cancelled. It reads batches in one goroutine and
// flushes closed buckets on a ticker in another, sharing the aggregator (which is
// concurrency-safe). Returns when ctx is done.
func (c *AnalyticsConsumer) Run(ctx context.Context) {
	if err := c.ensureGroup(ctx); err != nil {
		logger.Warn("analytics: consumer group setup failed; analytics disabled", "error", err)
		return
	}
	logger.Info("Miabi analytics consumer started", "stream", c.stream, "consumer", c.consumer)

	go c.flushLoop(ctx)

	for {
		if ctx.Err() != nil {
			return
		}
		res, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    analyticsGroup,
			Consumer: c.consumer,
			Streams:  []string{c.stream, ">"},
			Count:    500,
			Block:    5 * time.Second,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
				continue
			}
			// BUSYGROUP or transient error: back off briefly.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		var ids []string
		for _, stream := range res {
			for _, msg := range stream.Messages {
				c.ingestMessage(msg)
				ids = append(ids, msg.ID)
			}
		}
		if len(ids) > 0 {
			// Ack in-memory-accepted events; buckets persist on the flush ticker.
			if err := c.rdb.XAck(ctx, c.stream, analyticsGroup, ids...).Err(); err != nil {
				logger.Debug("analytics: XAck failed", "error", err)
			}
		}
	}
}

// ensureGroup creates the consumer group at the stream tail (MkStream), ignoring
// the BUSYGROUP error when it already exists.
func (c *AnalyticsConsumer) ensureGroup(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.stream, analyticsGroup, "$").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// ingestMessage parses one stream message and folds it into the aggregator.
func (c *AnalyticsConsumer) ingestMessage(msg redis.XMessage) {
	raw, ok := msg.Values["e"].(string)
	if !ok || raw == "" {
		return
	}
	var e analytics.Event
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return
	}
	if e.Route == "" {
		return
	}
	ws := analytics.WorkspaceIDFromRoute(e.Route)
	if ws == 0 {
		return // not a workspace route (e.g. the platform gateway's own route)
	}
	app := c.appFor(e.Route)
	c.agg.Ingest(&e, ws, app)
}

// appFor resolves a Goma route name to its application id via a short-lived cache
// of every route's Goma name. A miss (unknown route) returns 0 — the rollup still
// records workspace-level traffic, just without an app dimension.
func (c *AnalyticsConsumer) appFor(gomaRoute string) uint {
	if time.Since(c.routeLoaded) > analyticsRouteTTL {
		c.reloadRoutes()
	}
	return c.routeMap[gomaRoute]
}

func (c *AnalyticsConsumer) reloadRoutes() {
	all, err := c.routes.ListAll()
	if err != nil {
		c.routeLoaded = time.Now() // avoid hammering on persistent failure
		return
	}
	m := make(map[string]uint, len(all))
	for i := range all {
		rt := &all[i]
		m[proxy.GomaName(rt.WorkspaceID, rt.Name)] = rt.ApplicationID
	}
	c.routeMap = m
	c.routeLoaded = time.Now()
}

// flushLoop persists closed buckets on the flush interval and prunes old rollups
// roughly hourly.
func (c *AnalyticsConsumer) flushLoop(ctx context.Context) {
	flush := time.NewTicker(c.flushEvery)
	defer flush.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():
			c.flush(context.Background()) // final drain
			return
		case <-flush.C:
			c.flush(ctx)
		case <-prune.C:
			c.prune(ctx)
		}
	}
}

func (c *AnalyticsConsumer) flush(ctx context.Context) {
	before := time.Now().UTC().Add(-analyticsBucketGrace).Truncate(time.Minute)
	rows := c.agg.Flush(before)
	if len(rows) == 0 {
		return
	}
	if err := c.store.Upsert(rows); err != nil {
		logger.Warn("analytics: persist rollups failed", "buckets", len(rows), "error", err)
		return
	}
	logger.Debug("analytics: persisted rollups", "buckets", len(rows))
	_ = ctx
}

func (c *AnalyticsConsumer) prune(ctx context.Context) {
	if c.retentionDays == nil {
		return
	}
	days := c.retentionDays()
	if days <= 0 {
		return
	}
	before := time.Now().UTC().AddDate(0, 0, -days)
	if n, err := c.store.Prune(before); err == nil && n > 0 {
		logger.Info("analytics: pruned old rollups", "rows", n, "olderThan", before.Format("2006-01-02"))
	}
	_ = ctx
}
