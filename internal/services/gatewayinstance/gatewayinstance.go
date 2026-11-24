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

// Package gatewayinstance records which gateways poll a cluster's config and whether they hold the current one.
package gatewayinstance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/services/cluster"
	"github.com/redis/go-redis/v9"
)

const (
	EndpointCluster = "cluster"
	EndpointNode    = "node"

	StatusInSync  = "in_sync"
	StatusSyncing = "syncing"
	StatusOffline = "offline"
	// StatusNotServing is a gateway still polling from a node that is no longer the cluster's gateway node.
	StatusNotServing = "not_serving"

	// Gateways poll every 30s, so two missed polls plus slack mean it stopped polling.
	offlineAfter = 2 * time.Minute
	// A gateway gone this long is forgotten rather than listed as offline forever.
	forgetAfter = 7 * 24 * time.Hour
)

// Instance is one gateway's latest poll of a cluster's config.
type Instance struct {
	Gateway  string    `json:"gateway"`
	NodeID   uint      `json:"node_id,omitempty"`
	Endpoint string    `json:"endpoint"`
	RemoteIP string    `json:"remote_ip,omitempty"`
	Version  string    `json:"version,omitempty"`
	Checksum string    `json:"checksum"`
	SeenAt   time.Time `json:"seen_at"`
}

// View is an Instance judged against the config it would be served now.
type View struct {
	Instance
	Status  string `json:"status"`
	Current string `json:"current_checksum,omitempty"`
}

type Bundles interface {
	ClusterBundle(c *models.Cluster) ([]proxy.RenderedRoute, []proxy.RenderedMiddleware, error)
	NodeBundle(serverID uint) ([]proxy.RenderedRoute, []proxy.RenderedMiddleware, error)
}

// Keys returns the key each endpoint encrypts a bundle under, which the checksum covers.
type Keys interface {
	ClusterKey(c *models.Cluster) string
	NodeKey(serverID uint) string
}

type ClusterLookup interface {
	FindByID(id uint) (*models.Cluster, error)
}

type ServerLookup interface {
	FindByID(id uint) (*models.Server, error)
}

// Service is nil-safe: without Redis nothing is recorded and every cluster lists no gateways.
type Service struct {
	rdb      *redis.Client
	bundles  Bundles
	keys     Keys
	clusters ClusterLookup
	servers  ServerLookup
	now      func() time.Time
}

// SetServers lets an admin-chosen additional gateway count as serving its cluster.
func (s *Service) SetServers(l ServerLookup) {
	if s != nil {
		s.servers = l
	}
}

func (s *Service) serving(c *models.Cluster, nodeID uint) bool {
	if nodeID == 0 || nodeID == c.IngressNode() {
		return true
	}
	if s.servers == nil {
		return false
	}
	srv, err := s.servers.FindByID(nodeID)
	return err == nil && cluster.IsAdditionalGateway(c, srv)
}

func New(rdb *redis.Client, bundles Bundles, keys Keys, clusters ClusterLookup) *Service {
	if rdb == nil {
		return nil
	}
	return &Service{rdb: rdb, bundles: bundles, keys: keys, clusters: clusters, now: time.Now}
}

func clusterKey(clusterID uint) string { return fmt.Sprintf("miabi:gateways:cluster:%d", clusterID) }

func (s *Service) Record(ctx context.Context, clusterID uint, in Instance) {
	if s == nil || clusterID == 0 || in.Gateway == "" {
		return
	}
	in.SeenAt = s.now().UTC()
	raw, err := json.Marshal(in)
	if err != nil {
		return
	}
	key := clusterKey(clusterID)
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, key, in.Gateway, raw)
	pipe.Expire(ctx, key, forgetAfter)
	_, _ = pipe.Exec(ctx)
}

func (s *Service) list(ctx context.Context, clusterID uint) ([]Instance, error) {
	if s == nil {
		return nil, nil
	}
	key := clusterKey(clusterID)
	all, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]Instance, 0, len(all))
	for field, raw := range all {
		var in Instance
		if json.Unmarshal([]byte(raw), &in) != nil || now.Sub(in.SeenAt) > forgetAfter {
			s.rdb.HDel(ctx, key, field)
			continue
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Gateway < out[j].Gateway })
	return out, nil
}

// RecentNodes are the nodes whose gateways polled the cluster lately, so a reload reaches each of them.
func (s *Service) RecentNodes(ctx context.Context, clusterID uint) []uint {
	list, err := s.list(ctx, clusterID)
	if err != nil {
		return nil
	}
	var ids []uint
	for _, in := range list {
		if in.NodeID != 0 && s.now().Sub(in.SeenAt) <= offlineAfter {
			ids = append(ids, in.NodeID)
		}
	}
	return ids
}

func (s *Service) Status(ctx context.Context, clusterID uint) ([]View, error) {
	list, err := s.list(ctx, clusterID)
	if err != nil || len(list) == 0 {
		return []View{}, err
	}
	c, err := s.clusters.FindByID(clusterID)
	if err != nil {
		return nil, err
	}
	current := map[string]string{}
	out := make([]View, 0, len(list))
	for _, in := range list {
		ref := in.Endpoint + ":" + fmt.Sprint(in.NodeID)
		sum, ok := current[ref]
		if !ok {
			sum = s.currentChecksum(c, in)
			current[ref] = sum
		}
		v := View{Instance: in, Current: sum}
		switch {
		case s.now().Sub(in.SeenAt) > offlineAfter:
			v.Status = StatusOffline
		case !s.serving(c, in.NodeID):
			v.Status = StatusNotServing
		case sum != "" && in.Checksum == sum:
			v.Status = StatusInSync
		default:
			v.Status = StatusSyncing
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) currentChecksum(c *models.Cluster, in Instance) string {
	if in.Endpoint == EndpointCluster {
		routes, mws, err := s.bundles.ClusterBundle(c)
		if err != nil {
			return ""
		}
		return proxy.BundleChecksum(routes, mws, s.keys.ClusterKey(c))
	}
	if in.NodeID == 0 {
		return ""
	}
	routes, mws, err := s.bundles.NodeBundle(in.NodeID)
	if err != nil {
		return ""
	}
	return proxy.BundleChecksum(routes, mws, s.keys.NodeKey(in.NodeID))
}

// GomaVersion pulls the version out of Goma's "goma-gateway/<version>" User-Agent.
func GomaVersion(userAgent string) string {
	v, ok := strings.CutPrefix(userAgent, "goma-gateway/")
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}
