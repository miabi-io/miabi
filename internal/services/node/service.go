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

// Package node manages Docker host (server) records and their reachability.
package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/pkg/stack"
	"gorm.io/gorm"
)

var (
	ErrNodeNotFound            = errors.New("node not found")
	ErrNameRequired            = errors.New("node name is required")
	ErrLocalNode               = errors.New("the local node cannot be modified this way")
	ErrBadToken                = errors.New("invalid agent token")
	ErrNodeCordoned            = errors.New("node is cordoned and cannot accept new placements")
	ErrConnectivityAckRequired = &connectivityAckError{}
	ErrInvalidPool             = errors.New("a pool name is lowercase letters, digits and hyphens (max 32), e.g. pro")
	// Surfaced as a warning, not an abort: the cordon itself was saved.
	ErrSwarmNotMirrored    = errors.New("the node was updated, but its swarm scheduling availability could not be changed — Swarm may still place service tasks on it")
	ErrInvalidConnectivity = errors.New("a node either runs its own gateway (edge-gateway) or, as a swarm member, is served by its cluster's gateway (cluster)")
)

// NodeLimitError carries a stable Code() so the API surfaces NODE_LIMIT_REACHED with an upgrade-oriented message.
type NodeLimitError struct{ Limit int }

func (e *NodeLimitError) Error() string {
	return fmt.Sprintf("node limit reached: this edition is limited to %d nodes besides the control plane", e.Limit)
}
func (e *NodeLimitError) Code() string { return "NODE_LIMIT_REACHED" }
func (e *NodeLimitError) Message() string {
	return fmt.Sprintf("This edition is limited to %d nodes besides the control plane. Upgrade to Enterprise to add more.", e.Limit)
}

const tokenPrefix = "mbn_"

// AppNetwork is the gateway network Goma and app containers join; set once at startup via MIABI_PROXY_NETWORK.
var AppNetwork = stack.DefaultNetwork

// SetAppNetwork must be called once at startup, before Bootstrap or any deploy.
func SetAppNetwork(name string) {
	if name != "" {
		AppNetwork = name
	}
}

// AppAlias is the redeploy-stable upstream alias; legacy apps without a stored alias fall back to "mb-app-<id>".
func AppAlias(app *models.Application) string {
	if app.Alias != "" {
		return app.Alias
	}
	return fmt.Sprintf("mb-app-%d", app.ID)
}

// CanaryAlias differs from AppAlias so the proxy can split weighted traffic between stable and canary.
func CanaryAlias(app *models.Application) string {
	return AppAlias(app) + "-canary"
}

// NewAppAlias builds a fresh stable alias for a newly created app.
func NewAppAlias(token string, appID uint) string {
	return fmt.Sprintf("mb-app-%s-%d", token, appID)
}

// IngressOverlay carries only gateway-to-VIP traffic, so workspace east-west isolation is unaffected.
const IngressOverlay = "miabi-ingress"

type Service struct {
	repo      *repositories.ServerRepository
	clusters  *repositories.ClusterRepository
	docker    docker.Client
	nodeLimit func() int // resolved edition node cap (-1 = unlimited); nil = unlimited
	orphaned  func(serverID uint)
	// A closure keeps cluster.Service out of this package's imports (it already depends on this one).
	swarmCordon func(ctx context.Context, serverID uint, cordoned bool) error
}

// SetOrphanHandler wires the reconciler run after a node is deleted (nil-safe).
func (s *Service) SetOrphanHandler(fn func(serverID uint)) { s.orphaned = fn }

// SetSwarmCordon wires the swarm-availability mirror (nil-safe; nil leaves the cordon Miabi-side only).
func (s *Service) SetSwarmCordon(fn func(ctx context.Context, serverID uint, cordoned bool) error) {
	s.swarmCordon = fn
}

func NewService(repo *repositories.ServerRepository, dockerClient docker.Client) *Service {
	return &Service{repo: repo, docker: dockerClient}
}

// SetClusters wires cluster rows; nil leaves new nodes unassigned, which resolves to the default cluster.
func (s *Service) SetClusters(r *repositories.ClusterRepository) { s.clusters = r }

func (s *Service) placeInStandaloneCluster(srv *models.Server) error {
	if s.clusters == nil {
		return nil
	}
	name, err := slug.Unique(srv.Name, "node", func(c string) (bool, error) {
		_, e := s.clusters.FindByName(c)
		if e == nil {
			return true, nil
		}
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, e
	})
	if err != nil {
		return err
	}
	_, err = s.clusters.CreateStandalone(srv, name)
	return err
}

// SetNodeLimit's closure is re-evaluated per registration so license changes apply without restart; nil or negative is unlimited.
func (s *Service) SetNodeLimit(fn func() int) { s.nodeLimit = fn }

// Only the next registration is blocked; the control-plane host is not counted, matching the license view metric.
func (s *Service) checkNodeLimit() error {
	if s.nodeLimit == nil {
		return nil
	}
	limit := s.nodeLimit()
	if limit < 0 {
		return nil
	}
	n, err := s.repo.CountLicensed()
	if err != nil {
		return err
	}
	if n >= int64(limit) {
		return &NodeLimitError{Limit: limit}
	}
	return nil
}

// Bootstrap is non-fatal when Docker is unreachable, so the API can still serve.
func (s *Service) Bootstrap(ctx context.Context, endpoint string) {
	server, err := s.repo.EnsureLocal("manager", endpoint)
	if err != nil {
		logger.Error("failed to register manager node", "error", err)
		return
	}
	// The manager always uses the local socket, role and name "manager" (older installs used "local").
	dirty := false
	if server.AccessMode != models.AccessSocket {
		server.AccessMode = models.AccessSocket
		dirty = true
	}
	if server.Role != models.RoleManager {
		server.Role = models.RoleManager
		dirty = true
	}
	if server.DisplayName == "local" {
		server.DisplayName = "manager"
		dirty = true
	}
	if dirty {
		_ = s.repo.Update(server)
	}
	s.refreshStatus(ctx, server)

	if _, err := s.docker.EnsureNetwork(ctx, AppNetwork); err != nil {
		logger.Warn("failed to ensure docker network", "network", AppNetwork, "error", err)
	} else {
		logger.Info("docker network ready", "network", AppNetwork)
	}
}

// List returns all servers, refreshing the local node's live status.
func (s *Service) List(ctx context.Context) ([]models.Server, error) {
	servers, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	for i := range servers {
		if servers[i].IsLocal {
			s.refreshStatus(ctx, &servers[i])
		}
		servers[i].TLSEnabled = servers[i].TLSCert != "" || servers[i].TLSCACert != ""
	}
	return servers, nil
}

// Get returns a node by id.
func (s *Service) Get(id uint) (*models.Server, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	return srv, nil
}

// The TLS key is plaintext on input and encrypted at rest.
type NodeInput struct {
	// The handle is derived once and not settable: deployed edge gateways poll /api/v1/provider/{name}.
	DisplayName  string
	Address      string
	Connectivity models.ServerConnectivity
	AccessMode   models.ServerAccessMode
	// DockerEndpoint is the host for non-agent modes (unix://… / tcp://host:2376).
	DockerEndpoint string
	TLSCACert      string // api, optional (PEM)
	TLSCert        string // api, optional (PEM)
	TLSKey         string // api, optional (PEM; stored encrypted)
	// Required whenever the update changes reachability, which may briefly interrupt the node's workloads.
	Acknowledge bool
}

// CreateNode returns a one-time join token for agent mode (only its hash is stored).
func (s *Service) CreateNode(in NodeInput) (*models.Server, string, error) {
	if strings.TrimSpace(in.DisplayName) == "" {
		return nil, "", ErrNameRequired
	}
	if err := s.checkNodeLimit(); err != nil {
		return nil, "", err
	}
	mode := in.AccessMode
	if !models.ValidAccessMode(mode) {
		mode = models.AccessAgent
	}
	if in.Connectivity != "" && in.Connectivity != models.ConnectivityEdgeGateway {
		return nil, "", ErrInvalidConnectivity
	}
	in.Connectivity = models.ConnectivityEdgeGateway
	// slug.Unique suffixes a collision rather than failing.
	nodeSlug, err := slug.Unique(in.DisplayName, "node", func(c string) (bool, error) {
		_, e := s.repo.FindByName(c)
		if e == nil {
			return true, nil
		}
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, e
	})
	if err != nil {
		return nil, "", err
	}
	srv := &models.Server{
		Name:           nodeSlug,
		DisplayName:    strings.TrimSpace(in.DisplayName),
		Role:           models.RoleNode,
		Connectivity:   in.Connectivity,
		AccessMode:     mode,
		DockerEndpoint: strings.TrimSpace(in.DockerEndpoint),
		Address:        deriveAddress(mode, in.DockerEndpoint, in.Address),
		IsLocal:        false,
		Status:         models.ServerStatusOffline,
	}
	if err := applyCredentials(srv, in); err != nil {
		return nil, "", err
	}
	var token string
	if mode == models.AccessAgent {
		token = generateToken()
		srv.TokenHash = hashToken(token)
	}
	if err := s.repo.Create(srv); err != nil {
		return nil, "", err
	}
	if err := s.placeInStandaloneCluster(srv); err != nil {
		logger.Warn("created the node, but could not give it a standalone cluster", "node", srv.ID, "error", err)
	}
	return srv, token, nil
}

// UpdateNode replaces credential fields only when non-empty, so an unchanged form keeps existing secrets.
func (s *Service) UpdateNode(id uint, in NodeInput) (*models.Server, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	if err := s.checkConnectivity(srv, in.Connectivity); err != nil {
		return nil, err
	}
	if reachabilityChanged(srv, in) && !in.Acknowledge {
		return nil, ErrConnectivityAckRequired
	}
	if srv.IsLocal {
		if strings.TrimSpace(in.DisplayName) != "" {
			srv.DisplayName = strings.TrimSpace(in.DisplayName)
		}
		if validConnectivity(in.Connectivity) {
			srv.Connectivity = in.Connectivity
		}
		if err := s.repo.Update(srv); err != nil {
			return nil, err
		}
		return srv, nil
	}
	if strings.TrimSpace(in.DisplayName) != "" {
		srv.DisplayName = in.DisplayName
	}
	if models.ValidAccessMode(in.AccessMode) {
		srv.AccessMode = in.AccessMode
	}
	if validConnectivity(in.Connectivity) {
		srv.Connectivity = in.Connectivity
	}
	srv.DockerEndpoint = strings.TrimSpace(in.DockerEndpoint)
	srv.Address = deriveAddress(srv.AccessMode, in.DockerEndpoint, in.Address)
	if err := applyCredentials(srv, in); err != nil {
		return nil, err
	}
	if err := s.repo.Update(srv); err != nil {
		return nil, err
	}
	return srv, nil
}

func (s *Service) WorkloadImpact(id uint) (apps int64, databases int64, err error) {
	return s.repo.CountWorkloads(id)
}

// FindBySwarmNodeID resolves the node backing a swarm node id.
func (s *Service) FindBySwarmNodeID(swarmNodeID string) (*models.Server, error) {
	return s.repo.FindBySwarmNodeID(swarmNodeID)
}

// RegisterClusterNode is not open registration: the caller has verified the swarm node belongs to this swarm.
func (s *Service) RegisterClusterNode(clusterID uint, swarmNodeID, hostname string) (*models.Server, error) {
	if err := s.checkNodeLimit(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(hostname)
	if name == "" {
		name = "node-" + shortID(swarmNodeID)
	}
	srv := &models.Server{
		Name:         slug.Make(name, shortID(swarmNodeID)),
		DisplayName:  uniqueName(s.repo, name),
		IsLocal:      false,
		Role:         models.RoleNode,
		AccessMode:   models.AccessAgent,
		Connectivity: models.ConnectivityCluster,
		Status:       models.ServerStatusUnknown,
		SwarmNodeID:  swarmNodeID,
		AutoJoined:   true,
	}
	srv.ClusterID = clusterID
	if clusterID == models.DefaultClusterID && s.clusters != nil {
		if def, err := s.clusters.FindDefault(); err == nil {
			srv.ClusterID = def.ID
		}
	}
	if err := s.repo.Create(srv); err != nil {
		return nil, err
	}
	logger.Info("registered a swarm worker as a Miabi node",
		"node", srv.ID, "name", srv.Name, "swarm_node_id", swarmNodeID)
	return srv, nil
}

// uniqueName avoids colliding with an existing node's (unique) name.
func uniqueName(repo *repositories.ServerRepository, name string) string {
	servers, err := repo.List()
	if err != nil {
		return name
	}
	taken := map[string]bool{}
	for i := range servers {
		taken[strings.ToLower(servers[i].DisplayName)] = true
	}
	if !taken[strings.ToLower(name)] {
		return name
	}
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !taken[strings.ToLower(candidate)] {
			return candidate
		}
	}
	return name
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// LearnSwarmNodeID maps nodes joined by hand too; an unmapped node cannot be resolved from a service's task.
func (s *Service) LearnSwarmNodeID(id uint, swarmNodeID string) {
	swarmNodeID = strings.TrimSpace(swarmNodeID)
	if swarmNodeID == "" {
		return
	}
	srv, err := s.repo.FindByID(id)
	if err != nil || srv.SwarmNodeID == swarmNodeID {
		return
	}
	if err := s.SetSwarmNodeID(id, swarmNodeID); err != nil {
		logger.Warn("failed to persist the swarm node id reported by the agent",
			"node", id, "swarm_node_id", swarmNodeID, "error", err)
		return
	}
	logger.Info("learned a node's swarm id from its agent", "node", id, "swarm_node_id", swarmNodeID)
}

// PublicIP returns "" for non-routable addresses; raw may carry a port, which is stripped.
func PublicIP(raw string) string {
	host := strings.TrimSpace(raw)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return ""
	}
	return ip.String()
}

type connectivityAckError struct{}

func (*connectivityAckError) Error() string {
	return "changing node connectivity can interrupt running apps and databases; acknowledgement required"
}
func (*connectivityAckError) Code() string { return "CONNECTIVITY_ACK_REQUIRED" }

// validConnectivity reports whether c is one of the known connectivity modes.
func validConnectivity(c models.ServerConnectivity) bool {
	return c == models.ConnectivityEdgeGateway || c == models.ConnectivityCluster
}

// ErrEdgeGatewayInDefaultCluster is returned for a node of the default cluster set to run its own gateway.
var ErrEdgeGatewayInDefaultCluster = errors.New("a node of the default cluster is served by the control-plane gateway and cannot run one of its own; remove it from the swarm to give it its own cluster")

// Only the control plane or a swarm member can use its cluster's gateway; a default-cluster node cannot run its own.
func (s *Service) checkConnectivity(srv *models.Server, c models.ServerConnectivity) error {
	switch {
	case c == "" || c == srv.Connectivity:
		return nil
	case !validConnectivity(c):
		return ErrInvalidConnectivity
	case srv.IsLocal || s.clusters == nil:
		return nil
	}
	cl, err := s.clusters.FindByID(srv.ClusterID)
	switch {
	case err != nil:
		return ErrInvalidConnectivity
	case c == models.ConnectivityEdgeGateway && cl.IsDefault:
		return ErrEdgeGatewayInDefaultCluster
	case c == models.ConnectivityCluster && cl.Mode != models.ClusterModeSwarm:
		return ErrInvalidConnectivity
	}
	return nil
}

// SetPool assigns a node to a pool (empty for none); pool-bound plans place workloads only on that pool's nodes.
func (s *Service) SetPool(id uint, pool string) (*models.Server, error) {
	pool = strings.TrimSpace(pool)
	if pool != "" && !models.ValidPoolName(pool) {
		return nil, ErrInvalidPool
	}
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	if srv.Labels == nil {
		srv.Labels = map[string]string{}
	}
	if pool == "" {
		delete(srv.Labels, models.PoolLabel)
	} else {
		srv.Labels[models.PoolLabel] = pool
	}
	if err := s.repo.Update(srv); err != nil {
		return nil, err
	}
	return srv, nil
}

// SetConnectivity changes only how a node's apps are served, leaving its other reachability settings alone.
func (s *Service) SetConnectivity(id uint, c models.ServerConnectivity) (*models.Server, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	if err := s.checkConnectivity(srv, c); err != nil {
		return nil, err
	}
	srv.Connectivity = c
	if err := s.repo.Update(srv); err != nil {
		return nil, err
	}
	return srv, nil
}

// Mirrors what UpdateNode applies, so a metadata-only edit resending current values is not a reachability change.
func reachabilityChanged(srv *models.Server, in NodeInput) bool {
	if validConnectivity(in.Connectivity) && in.Connectivity != srv.Connectivity {
		return true
	}
	if srv.IsLocal {
		return false
	}
	if models.ValidAccessMode(in.AccessMode) && in.AccessMode != srv.AccessMode {
		return true
	}
	if strings.TrimSpace(in.DockerEndpoint) != srv.DockerEndpoint {
		return true
	}
	mode := srv.AccessMode
	if models.ValidAccessMode(in.AccessMode) {
		mode = in.AccessMode
	}
	if deriveAddress(mode, in.DockerEndpoint, in.Address) != srv.Address {
		return true
	}
	// New TLS material rotates the client credentials — treated as impactful.
	return strings.TrimSpace(in.TLSCACert) != "" ||
		strings.TrimSpace(in.TLSCert) != "" ||
		strings.TrimSpace(in.TLSKey) != ""
}

func deriveAddress(mode models.ServerAccessMode, endpoint, address string) string {
	if mode == models.AccessAPI {
		if u, err := url.Parse(strings.TrimSpace(endpoint)); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return strings.TrimSpace(address)
}

// Blank means keep: the edit form never receives stored secrets back.
func applyCredentials(srv *models.Server, in NodeInput) error {
	if strings.TrimSpace(in.TLSCACert) != "" {
		srv.TLSCACert = in.TLSCACert
	}
	if strings.TrimSpace(in.TLSCert) != "" {
		srv.TLSCert = in.TLSCert
	}
	if strings.TrimSpace(in.TLSKey) != "" {
		enc, err := crypto.Encrypt(in.TLSKey)
		if err != nil {
			return err
		}
		srv.TLSKeyEnc = enc
	}
	return nil
}

// RegenerateToken issues a fresh join token for a node, invalidating the old one.
func (s *Service) RegenerateToken(id uint) (string, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return "", ErrNodeNotFound
	}
	if srv.IsLocal {
		return "", ErrLocalNode
	}
	token := generateToken()
	srv.TokenHash = hashToken(token)
	if err := s.repo.Update(srv); err != nil {
		return "", err
	}
	return token, nil
}

// GatewayToken mints on first use; unlike the join token it is recoverable, so the gateway can be redeployed.
func (s *Service) GatewayToken(id uint) (string, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return "", ErrNodeNotFound
	}
	if srv.GatewayTokenEnc != "" {
		if tok, derr := crypto.Decrypt(srv.GatewayTokenEnc); derr == nil && tok != "" {
			return tok, nil
		}
	}
	tok := generateToken()
	enc, err := crypto.Encrypt(tok)
	if err != nil {
		return "", err
	}
	srv.GatewayTokenEnc = enc
	if err := s.repo.Update(srv); err != nil {
		return "", err
	}
	return tok, nil
}

// GatewayRedisPassword mints on first use and stays stable so gateway and Redis credentials keep matching.
func (s *Service) GatewayRedisPassword(id uint) (string, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return "", ErrNodeNotFound
	}
	if srv.GatewayRedisPasswordEnc != "" {
		if pw, derr := crypto.Decrypt(srv.GatewayRedisPasswordEnc); derr == nil && pw != "" {
			return pw, nil
		}
	}
	pw := generateToken()
	enc, err := crypto.Encrypt(pw)
	if err != nil {
		return "", err
	}

	stored, err := s.repo.ClaimGatewayRedisPassword(id, enc, srv.GatewayRedisPasswordEnc != "")
	if err != nil {
		return "", err
	}
	if !stored {
		return s.GatewayRedisPassword(id)
	}
	return pw, nil
}

// StoredGatewayRedisPassword returns the node's gateway Redis password without minting one
func (s *Service) StoredGatewayRedisPassword(id uint) (string, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return "", ErrNodeNotFound
	}
	if srv.GatewayRedisPasswordEnc == "" {
		return "", nil
	}
	pw, err := crypto.Decrypt(srv.GatewayRedisPasswordEnc)
	if err != nil {
		return "", nil
	}
	return pw, nil
}

// SetGatewayUpdate persists safe-update progress (nil clears) so it survives a reconnect.
func (s *Service) SetGatewayUpdate(id uint, p *models.GatewayUpdateProgress) error {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return ErrNodeNotFound
	}
	srv.GatewayUpdate = p
	return s.repo.Update(srv)
}

// SetGatewayConfig stores a custom edge-gateway config; empty resets to the rendered default.
func (s *Service) SetGatewayConfig(id uint, config string) (*models.Server, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	srv.GatewayConfigYAML = config
	if err := s.repo.Update(srv); err != nil {
		return nil, err
	}
	return srv, nil
}

// SetGatewayImage stores an edge-gateway image override; empty resets to the resolved default.
func (s *Service) SetGatewayImage(id uint, image string) (*models.Server, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	srv.GatewayImage = strings.TrimSpace(image)
	if err := s.repo.Update(srv); err != nil {
		return nil, err
	}
	return srv, nil
}

// MarkGatewayDeployed also clears imported-gateway tracking, since a fresh install supersedes an import.
func (s *Service) MarkGatewayDeployed(id uint) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return
	}
	now := time.Now()
	srv.GatewayDeployedAt = &now
	srv.GatewayContainer = ""
	srv.GatewayImported = false
	_ = s.repo.Update(srv)
}

// AdoptGateway tracks an existing gateway container without recreating it; nodes that cannot run a gateway are refused.
func (s *Service) AdoptGateway(id uint, container, image, configYAML string) (*models.Server, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	if err := s.checkConnectivity(srv, models.ConnectivityEdgeGateway); err != nil {
		return nil, err
	}
	srv.Connectivity = models.ConnectivityEdgeGateway
	srv.GatewayContainer = strings.TrimSpace(container)
	srv.GatewayImported = true
	if img := strings.TrimSpace(image); img != "" {
		srv.GatewayImage = img
	}
	if cfg := strings.TrimSpace(configYAML); cfg != "" {
		srv.GatewayConfigYAML = cfg
	}
	now := time.Now()
	srv.GatewayDeployedAt = &now
	if err := s.repo.Update(srv); err != nil {
		return nil, err
	}
	return srv, nil
}

// ReleaseGateway stops tracking an imported gateway without touching the container; copied settings remain.
func (s *Service) ReleaseGateway(id uint) (*models.Server, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	srv.GatewayContainer = ""
	srv.GatewayImported = false
	srv.GatewayDeployedAt = nil
	if err := s.repo.Update(srv); err != nil {
		return nil, err
	}
	return srv, nil
}

// AuthenticateProvider accepts the agent join token or the gateway token; the URL slug must match the node.
func (s *Service) AuthenticateProvider(slug, token string) (*models.Server, error) {
	srv, err := s.repo.FindByName(slug)
	if err != nil {
		return nil, ErrBadToken
	}
	if token == "" {
		return nil, ErrBadToken
	}
	// Agent join token (hashed) …
	if subtle.ConstantTimeCompare([]byte(srv.TokenHash), []byte(hashToken(token))) == 1 {
		return srv, nil
	}
	// … or the recoverable gateway token.
	if srv.GatewayTokenEnc != "" {
		if tok, derr := crypto.Decrypt(srv.GatewayTokenEnc); derr == nil && tok != "" &&
			subtle.ConstantTimeCompare([]byte(tok), []byte(token)) == 1 {
			return srv, nil
		}
	}
	return nil, ErrBadToken
}

// Placeable: the local node (0) always is; reachability is enforced separately when the operation runs.
func (s *Service) Placeable(serverID uint) error {
	if serverID == 0 {
		return nil
	}
	srv, err := s.repo.FindByID(serverID)
	if err != nil {
		return ErrNodeNotFound
	}
	if srv.IsLocal {
		return nil
	}
	if srv.Cordoned {
		return ErrNodeCordoned
	}
	return nil
}

// SetSwarmNodeID is a no-op when unchanged, avoiding write churn on every cluster refresh.
func (s *Service) SetSwarmNodeID(id uint, swarmNodeID string) error {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return ErrNodeNotFound
	}
	if srv.SwarmNodeID == swarmNodeID {
		return nil
	}
	// Column-scoped: MarkConnected writes the row on the same connect, and two full Saves would race.
	return s.repo.UpdateSwarmNodeID(id, swarmNodeID)
}

// NameBySwarmNodeID returns "" when no node carries the id; the caller falls back to a short id.
func (s *Service) NameBySwarmNodeID(swarmNodeID string) string {
	if strings.TrimSpace(swarmNodeID) == "" {
		return ""
	}
	servers, err := s.repo.List()
	if err != nil {
		return ""
	}
	for i := range servers {
		if servers[i].SwarmNodeID == swarmNodeID {
			return servers[i].Label()
		}
	}
	return ""
}

// SetCordoned mirrors to swarm availability; on mirror failure it saves anyway and returns ErrSwarmNotMirrored.
func (s *Service) SetCordoned(ctx context.Context, id uint, cordoned bool) error {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return ErrNodeNotFound
	}
	srv.Cordoned = cordoned
	if err := s.repo.Update(srv); err != nil {
		return err
	}
	if s.swarmCordon == nil {
		return nil
	}
	if err := s.swarmCordon(ctx, srv.ID, cordoned); err != nil {
		logger.Warn("node cordon was not mirrored to the swarm scheduler", "node", srv.ID, "cordoned", cordoned, "error", err)
		return fmt.Errorf("%w: %v", ErrSwarmNotMirrored, err)
	}
	return nil
}

// DeleteNode removes a remote node record.
func (s *Service) DeleteNode(id uint) error {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return ErrNodeNotFound
	}
	if srv.IsLocal {
		return ErrLocalNode
	}
	if err := s.repo.Delete(srv.ID); err != nil {
		return err
	}

	if s.orphaned != nil {
		s.orphaned(srv.ID)
	}
	if s.clusters != nil && srv.ClusterID != models.DefaultClusterID {
		if err := s.clusters.DeleteIfEmpty(srv.ClusterID); err != nil {
			logger.Warn("deleted the node, but not its empty cluster", "node", srv.ID, "cluster", srv.ClusterID, "error", err)
		}
	}
	return nil
}

func (s *Service) Authenticate(token string) (*models.Server, error) {
	srv, err := s.repo.FindByTokenHash(hashToken(token))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBadToken
		}
		return nil, err
	}
	// Defense in depth: re-verify the hash in constant time.
	if subtle.ConstantTimeCompare([]byte(srv.TokenHash), []byte(hashToken(token))) != 1 {
		return nil, ErrBadToken
	}
	return srv, nil
}

// MarkConnected records that a node's agent connected.
func (s *Service) MarkConnected(id uint, agentVersion string) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return
	}
	now := time.Now()
	srv.Status = models.ServerStatusOnline
	srv.LastSeenAt = &now
	if agentVersion != "" {
		srv.AgentVersion = agentVersion
	}
	_ = s.repo.Update(srv)
}

// SetEngineVersion is keyed by swarm node id and column-scoped so it never clobbers concurrent agent-connect writes.
func (s *Service) SetEngineVersion(swarmNodeID, version string) error {
	if swarmNodeID == "" || version == "" {
		return nil
	}
	return s.repo.UpdateEngineVersionBySwarmNodeID(swarmNodeID, version)
}

// MarkDisconnected records that a node's agent dropped.
func (s *Service) MarkDisconnected(id uint) {
	srv, err := s.repo.FindByID(id)
	if err != nil || srv.IsLocal {
		return
	}
	srv.Status = models.ServerStatusOffline
	_ = s.repo.Update(srv)
}

func generateToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return tokenPrefix + hex.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) refreshStatus(ctx context.Context, server *models.Server) {
	status := models.ServerStatusOffline
	if err := s.docker.Ping(ctx); err == nil {
		status = models.ServerStatusOnline
		now := time.Now()
		server.LastSeenAt = &now
	}
	server.Status = status
	if err := s.repo.Update(server); err != nil {
		logger.Error("failed to update server status", "error", err)
	}
}

// IDByUID resolves a node's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }
