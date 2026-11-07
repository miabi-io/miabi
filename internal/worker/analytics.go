// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
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
	// Shared by every worker so each gateway event is rolled up exactly once.
	analyticsGroup = "miabi-analytics"
	// Keeps the current and just-past minute open so late events still land in their bucket.
	analyticsBucketGrace = 90 * time.Second
	// analyticsRouteTTL bounds how stale the route→app reverse map may get.
	analyticsRouteTTL    = time.Minute
	analyticsStopTimeout = 10 * time.Second
)

// AnalyticsConsumer rolls Goma's per-request event stream into minute buckets; it holds no per-request rows or PII.
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

// consumer must be unique within the group so pending-message ownership is per-worker; nil retentionDays disables pruning.
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

// Run returns only after the final flush, so a caller can wait before closing the database.
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

// Start returns a stop func that waits for the final flush (bounded by analyticsStopTimeout); call before closing the DB.
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

func (c *AnalyticsConsumer) ensureGroup(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.stream, analyticsGroup, "$").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

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

	// Bots are left out so a crawler sweeping the site doesn't read as an audience.
	if e.VID == "" || analytics.IsBotUA(e.UA) {
		return analytics.LiveVisit{}, false
	}
	return analytics.LiveVisit{Workspace: ws, App: app, VID: e.VID}, true
}

// An unknown route returns 0; the rollup still records workspace-level traffic.
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
