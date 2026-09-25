// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/nodestats"
	"github.com/miabi-io/miabi/pkg/hoststats"
)

const (
	// agentStatsMaxBody caps the pre-auth read: a report is ~100 bytes.
	agentStatsMaxBody = 4 << 10
	// clusterAgentAuthTTL spares the swarm manager a membership lookup on every 15s push.
	clusterAgentAuthTTL = 5 * time.Minute
)

// NodeStatsIngester accepts host stats pushed by a node's agent. Satisfied by nodestats.Service.
type NodeStatsIngester interface {
	Ingest(srv *models.Server, r hoststats.Report) error
}

// AgentStatsRequest documents the pushed report.
type AgentStatsRequest struct {
	Body hoststats.Report `json:"body"`
}

// AgentStatsResponse acknowledges a pushed report.
type AgentStatsResponse struct {
	Accepted bool `json:"accepted"`
}

type clusterAgentAuthCache struct {
	mu      sync.Mutex
	entries map[string]clusterAgentAuthEntry
}

type clusterAgentAuthEntry struct {
	serverID uint
	expires  time.Time
}

func (c *clusterAgentAuthCache) get(key string) (uint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.entries, key)
		return 0, false
	}
	return e.serverID, true
}

func (c *clusterAgentAuthCache) put(key string, id uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]clusterAgentAuthEntry{}
	}
	c.entries[key] = clusterAgentAuthEntry{serverID: id, expires: time.Now().Add(clusterAgentAuthTTL)}
}

func (c *clusterAgentAuthCache) drop(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// AgentStats ingests a host CPU/memory report pushed by a node's agent. The node is identified by
// the bearer token — per-node or cluster-wide plus swarm node id, as for Connect — never by the body,
// and the reading is stamped with the control plane's clock.
func (h *NodeHandler) AgentStats(c *okapi.Context) error {
	ingest, _ := h.nodeStats.(NodeStatsIngester)
	if ingest == nil {
		return c.AbortNotFound("agent stats are not enabled on this instance")
	}
	token := bearer(c.Header("Authorization"))
	if token == "" {
		return c.AbortUnauthorized("invalid agent token")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, agentStatsMaxBody)

	srv, err := h.statsAgent(c, token)
	if errors.Is(err, errNodeGone) {
		return ok(c, AgentStatsResponse{})
	}
	if err != nil {
		return c.AbortUnauthorized("invalid agent token")
	}

	var report hoststats.Report
	if err := json.NewDecoder(c.Request().Body).Decode(&report); err != nil {
		return c.AbortBadRequest("invalid stats report")
	}
	if srv.IsLocal {
		return ok(c, AgentStatsResponse{})
	}
	if err := ingest.Ingest(srv, report); err != nil {
		if errors.Is(err, nodestats.ErrInvalidReport) {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to record stats", err)
	}
	return ok(c, AgentStatsResponse{Accepted: true})
}

var errNodeGone = errors.New("node no longer exists")

// statsAgent resolves the pushing agent's node the same way Connect does. The cluster-token path is
// cached briefly, since verifying it asks the swarm manager for its membership list.
func (h *NodeHandler) statsAgent(c *okapi.Context, token string) (*models.Server, error) {
	if srv, err := h.nodes.Authenticate(token); err == nil {
		return srv, nil
	}
	swarmNodeID := c.Header("X-Agent-Swarm-Node-ID")
	sum := sha256.Sum256([]byte(token + "\x00" + swarmNodeID))
	key := hex.EncodeToString(sum[:])
	if id, hit := h.clusterAuth.get(key); hit {
		srv, err := h.nodes.Get(id)
		if err != nil {
			h.clusterAuth.drop(key)
			return nil, errNodeGone
		}
		return srv, nil
	}
	srv, err := h.registerClusterAgent(c, token, swarmNodeID, c.Header("X-Agent-Hostname"))
	if err != nil {
		return nil, err
	}
	h.clusterAuth.put(key, srv.ID)
	return srv, nil
}
