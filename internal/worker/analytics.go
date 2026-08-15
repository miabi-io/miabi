// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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
	analyticsRouteTTL    = time.Minute
	analyticsStopTimeout = 10 * time.Second
)

// AnalyticsConsumer reads Goma Gateway's per-request event stream, rolls events into minute
// buckets and persists closed buckets on an interval. It resolves each event's workspace from
// the route name and its app from a cached route->app map. Holds no per-request rows and no PII.
type AnalyticsConsumer struct {
	rdb    *redis.Client
	routes *repositories.RouteRepository
	store  *repositories.AnalyticsRepository
	agg    *analytics.Aggregator

	live     *analytics.LiveTracker
	stream   string
	consumer string

	flushEvery    time.Duration
	retentionDays func() int

	routeMap    map[string]uint
	routeLoaded time.Time
}

// NewAnalyticsConsumer wires the consumer. consumer is this worker's unique name within the group,
// so pending-message ownership is per-worker. retentionDays is evaluated on each prune; nil
// disables pruning. live is passed in rather than built here, so the window is configured once.
func NewAnalyticsConsumer(rdb *redis.Client, routes *repositories.RouteRepository, store *repositories.AnalyticsRepository, stream, consumer string, flushEvery time.Duration, retentionDays func() int, live *analytics.LiveTracker) *AnalyticsConsumer {
	if flushEvery <= 0 {
		flushEvery = 15 * time.Second
	}
	if live == nil {
		live = analytics.NewLiveTracker(rdb, analytics.DefaultLiveWindow)
	}
	return &AnalyticsConsumer{
		rdb: rdb, routes: routes, store: store, agg: analytics.NewAggregator(),
		live:   live,
		stream: stream, consumer: consumer,
		flushEvery: flushEvery, retentionDays: retentionDays,
		routeMap: map[string]uint{},
	}
}

// Run consumes until ctx is cancelled, reading batches in one goroutine and flushing closed
// buckets on a ticker in another. It returns only once the final flush completed, so a caller can
// wait before closing the database — buckets live in memory until their minute closes.
func (c *AnalyticsConsumer) Run(ctx context.Context) {
	if err := c.ensureGroup(ctx); err != nil {
		logger.Warn("analytics: consumer group setup failed; analytics disabled", "error", err)
		return
	}
	logger.Info("Miabi analytics consumer started", "stream", c.stream, "consumer", c.consumer)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.flushLoop(ctx)
	}()
	defer wg.Wait()

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
		var visits []analytics.LiveVisit
		for _, stream := range res {
			for _, msg := range stream.Messages {
				if v, ok := c.ingestMessage(msg); ok {
					visits = append(visits, v)
				}
				ids = append(ids, msg.ID)
			}
		}
		// One write per stream read rather than one per event.
		c.live.Touch(ctx, visits)
		if len(ids) > 0 {
			// Ack in-memory-accepted events; buckets persist on the flush ticker.
			if err := c.rdb.XAck(ctx, c.stream, analyticsGroup, ids...).Err(); err != nil {
				logger.Debug("analytics: XAck failed", "error", err)
			}
		}
	}
}

// Start runs the consumer in the background and returns a function that stops it and waits for the
// final flush. Call it during shutdown, before closing the database. It gives up after
// analyticsStopTimeout so a stuck flush can't block exit. Safe to call more than once.
func (c *AnalyticsConsumer) Start(ctx context.Context) func() {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(runCtx)
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
				logger.Debug("analytics: consumer stopped")
			case <-time.After(analyticsStopTimeout):
				logger.Warn("analytics: consumer did not stop in time; open buckets may be lost",
					"timeout", analyticsStopTimeout)
			}
		})
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

// ingestMessage rolls one event into its bucket and reports the visitor sighting the caller should
// record, if the event counts towards live visitors.
func (c *AnalyticsConsumer) ingestMessage(msg redis.XMessage) (analytics.LiveVisit, bool) {
	raw, ok := msg.Values[analytics.StreamEventField].(string)
	if !ok || raw == "" {
		return analytics.LiveVisit{}, false
	}
	var e analytics.Event
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return analytics.LiveVisit{}, false
	}
	if e.Route == "" {
		return analytics.LiveVisit{}, false
	}
	ws := analytics.WorkspaceIDFromRoute(e.Route)
	if ws == 0 {
		return analytics.LiveVisit{}, false // not a workspace route (e.g. the platform gateway's own)
	}
	app := c.appFor(e.Route)
	c.agg.Ingest(&e, ws, app)

	// Live visitors counts people, so automated traffic is left out — a crawler
	// sweeping the site shouldn't read as an audience.
	if e.VID == "" || analytics.IsBotUA(e.UA) {
		return analytics.LiveVisit{}, false
	}
	return analytics.LiveVisit{Workspace: ws, App: app, VID: e.VID}, true
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
			c.live.Sweep(ctx)
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
