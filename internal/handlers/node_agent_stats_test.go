// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/nodestats"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type agentStatsServerTable struct {
	ID          uint `gorm:"primaryKey"`
	Name        string
	TokenHash   string
	IsLocal     bool
	MemoryBytes int64
}

func (agentStatsServerTable) TableName() string { return "servers" }

// fakeClusterAgents stands in for cluster.Service: one cluster token, one swarm member.
type fakeClusterAgents struct {
	token, swarmNodeID string
	serverID           uint
	db                 *gorm.DB
	calls              int
}

func (f *fakeClusterAgents) Enrich([]models.Server)      {}
func (f *fakeClusterAgents) LearnIngressIP(uint, string) {}
func (f *fakeClusterAgents) AuthenticateAgent(_ context.Context, token, swarmNodeID, _ string) (*models.Server, error) {
	f.calls++
	if token != f.token || swarmNodeID != f.swarmNodeID {
		return nil, errors.New("not a member")
	}
	return repositories.NewServerRepository(f.db).FindByID(f.serverID)
}

const (
	nodeToken    = "mbn_node_token"
	clusterToken = "mbc_cluster_token"
	gib          = int64(1) << 30
)

func agentStatsHandler(t *testing.T) (*NodeHandler, *nodestats.Service, *fakeClusterAgents, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&agentStatsServerTable{}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(nodeToken))
	for _, s := range []agentStatsServerTable{
		{ID: 1, Name: "edge-38", TokenHash: hex.EncodeToString(sum[:]), MemoryBytes: 8 * gib},
		{ID: 2, Name: "swarm-worker", MemoryBytes: 8 * gib},
	} {
		if err := db.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}
	stats := nodestats.NewService(nil)
	cluster := &fakeClusterAgents{token: clusterToken, swarmNodeID: "swarm-abc", serverID: 2, db: db}
	h := &NodeHandler{
		nodes:     node.NewService(repositories.NewServerRepository(db), nil),
		cluster:   cluster,
		nodeStats: stats,
	}
	return h, stats, cluster, db
}

func pushStats(h *NodeHandler, token, swarmNodeID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/stats", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if swarmNodeID != "" {
		req.Header.Set("X-Agent-Swarm-Node-ID", swarmNodeID)
	}
	rec := httptest.NewRecorder()
	_ = h.AgentStats(okapi.NewContext(okapi.New(), rec, req))
	return rec
}

const goodReport = `{"cpu_percent":12.5,"mem_total_bytes":8589934592,"mem_used_bytes":2147483648,"load1":0.3,"uptime_s":99}`

func TestAgentStatsPerNodeToken(t *testing.T) {
	h, stats, _, _ := agentStatsHandler(t)
	if rec := pushStats(h, nodeToken, "", goodReport); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got, ok := stats.Cached(1)
	if !ok || got.Source != nodestats.SourceAgent || got.CPUPercent != 12.5 || !got.DescribesNode {
		t.Fatalf("sample = %+v ok=%v", got, ok)
	}
}

// A swarm global-service agent carries the cluster token; nodes.Authenticate alone would 401 it.
func TestAgentStatsClusterTokenWithSwarmNodeID(t *testing.T) {
	h, stats, cluster, _ := agentStatsHandler(t)
	for i := 0; i < 3; i++ {
		if rec := pushStats(h, clusterToken, "swarm-abc", goodReport); rec.Code != http.StatusOK {
			t.Fatalf("push %d: status = %d: %s", i, rec.Code, rec.Body)
		}
	}
	if !stats.HasFreshPush(2) {
		t.Fatal("the swarm node's push was not recorded")
	}
	if cluster.calls != 1 {
		t.Fatalf("manager membership checks = %d, want 1 (cached after the first)", cluster.calls)
	}

	if rec := pushStats(h, clusterToken, "swarm-other", goodReport); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown swarm node: status = %d, want 401", rec.Code)
	}
}

func TestAgentStatsDropsPushesForADeletedNode(t *testing.T) {
	h, _, _, db := agentStatsHandler(t)
	if rec := pushStats(h, clusterToken, "swarm-abc", goodReport); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if err := db.Delete(&agentStatsServerTable{}, 2).Error; err != nil {
		t.Fatal(err)
	}
	rec := pushStats(h, clusterToken, "swarm-abc", goodReport)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"accepted":false`) {
		t.Fatalf("status = %d body = %s, want a silent 200", rec.Code, rec.Body)
	}
}

func TestAgentStatsRejects(t *testing.T) {
	cases := []struct {
		name, token, body string
		want              int
	}{
		{"no token", "", goodReport, http.StatusUnauthorized},
		{"unknown token", "mbn_nope", goodReport, http.StatusUnauthorized},
		{"not json", nodeToken, "cpu=5", http.StatusBadRequest},
		{"cpu out of range", nodeToken, `{"cpu_percent":140,"mem_total_bytes":10,"mem_used_bytes":1}`, http.StatusBadRequest},
		{"used over total", nodeToken, `{"cpu_percent":1,"mem_total_bytes":10,"mem_used_bytes":11}`, http.StatusBadRequest},
		{"zero total", nodeToken, `{"cpu_percent":1,"mem_total_bytes":0,"mem_used_bytes":0}`, http.StatusBadRequest},
		{"oversized", nodeToken, `{"cpu_percent":1,"pad":"` + strings.Repeat("x", agentStatsMaxBody) + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, stats, _, _ := agentStatsHandler(t)
			if rec := pushStats(h, tc.token, "", tc.body); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if stats.HasFreshPush(1) {
				t.Fatal("a rejected push was recorded")
			}
		})
	}
}
