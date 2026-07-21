// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	"gorm.io/gorm"
)

var (
	ErrNodeNotFound = errors.New("node not found")
	ErrNameRequired = errors.New("node name is required")
	ErrLocalNode    = errors.New("the local node cannot be modified this way")
	ErrBadToken     = errors.New("invalid agent token")
	ErrNodeCordoned = errors.New("node is cordoned and cannot accept new placements")
	// ErrConnectivityAckRequired is returned when an update changes the node's
	// reachability settings without the caller acknowledging the impact.
	ErrConnectivityAckRequired = &connectivityAckError{}
)

// NodeLimitError is returned when registering a node would exceed the edition's
// node cap. It exposes a stable Code() and a curated Message() so the API
// envelope surfaces NODE_LIMIT_REACHED with an upgrade-oriented message rather
// than the generic status word.
type NodeLimitError struct{ Limit int }

func (e *NodeLimitError) Error() string {
	return fmt.Sprintf("node limit reached: this edition is limited to %d nodes", e.Limit)
}
func (e *NodeLimitError) Code() string { return "NODE_LIMIT_REACHED" }
func (e *NodeLimitError) Message() string {
	return fmt.Sprintf("This edition is limited to %d nodes. Upgrade to Enterprise to add more.", e.Limit)
}

// tokenPrefix marks Miabi agent join tokens.
const tokenPrefix = "mbn_"

// AppNetwork is the shared Docker network Goma Gateway and managed app
// containers join (the proxy gateway network). Configurable via
// MIABI_PROXY_NETWORK; set once at startup with SetAppNetwork.
var AppNetwork = "miabi"

// SetAppNetwork overrides the gateway network name (call once at startup,
// before Bootstrap or any deploy).
func SetAppNetwork(name string) {
	if name != "" {
		AppNetwork = name
	}
}

// AppAlias is the stable network DNS alias for an application's active
// container, used as the reverse-proxy upstream so it survives redeploys. It is
// the app's stored Alias ("mb-app-<token>-<id>"); legacy apps with no stored
// alias fall back to "mb-app-<id>".
func AppAlias(app *models.Application) string {
	if app.Alias != "" {
		return app.Alias
	}
	return fmt.Sprintf("mb-app-%d", app.ID)
}

// CanaryAlias is the network DNS alias for an application's canary container,
// run alongside the stable release during a canary deployment. Only the stable
// release answers AppAlias, so the proxy can split weighted traffic between the
// two distinct aliases.
func CanaryAlias(app *models.Application) string {
	return AppAlias(app) + "-canary"
}

// NewAppAlias builds a fresh stable alias for a newly created app.
func NewAppAlias(token string, appID uint) string {
	return fmt.Sprintf("mb-app-%s-%d", token, appID)
}

// IngressOverlay is the single shared, attachable Swarm overlay the central Goma
// gateway joins to reach every clustered app's service VIP for public (north-
// south) ingress. Cluster (service) apps attach to it in addition to their
// per-workspace east-west overlay. Because it carries only gateway↔VIP traffic
// (each service registers just its globally-unique upstream alias here, not its
// tenant-scoped name), east-west isolation between workspaces is unaffected.
const IngressOverlay = "miabi-ingress"

type Service struct {
	repo      *repositories.ServerRepository
	docker    docker.Client
	nodeLimit func() int // resolved edition node cap (-1 = unlimited); nil = unlimited
}

func NewService(repo *repositories.ServerRepository, dockerClient docker.Client) *Service {
	return &Service{repo: repo, docker: dockerClient}
}

// SetNodeLimit wires the resolved edition node cap (manager + remotes). The
// closure is re-evaluated on every node registration so a license install or
// lapse takes effect without a restart. A nil closure or a negative return
// means unlimited.
func (s *Service) SetNodeLimit(fn func() int) { s.nodeLimit = fn }

// checkNodeLimit refuses a new node registration once the edition cap is
// reached. Already-registered nodes keep operating; only adding the next node is
// blocked. Counts every server row (manager + remotes), matching the node-usage
// metric shown in the license view.
func (s *Service) checkNodeLimit() error {
	if s.nodeLimit == nil {
		return nil
	}
	limit := s.nodeLimit()
	if limit < 0 {
		return nil
	}
	servers, err := s.repo.List()
	if err != nil {
		return err
	}
	if len(servers) >= limit {
		return &NodeLimitError{Limit: limit}
	}
	return nil
}

// Bootstrap registers the local node, pings the daemon to set its status, and
// ensures the managed Docker network exists. Non-fatal: logs and continues if
// Docker is unreachable so the API can still serve.
func (s *Service) Bootstrap(ctx context.Context, endpoint string) {
	server, err := s.repo.EnsureLocal("manager", endpoint)
	if err != nil {
		logger.Error("failed to register manager node", "error", err)
		return
	}
	// Normalize the manager node: it always reaches Docker via its local socket,
	// its role is "manager", and its display name is "manager" (older installs
	// were created as "local"). Persist any drift once.
	dirty := false
	if server.AccessMode != models.AccessSocket {
		server.AccessMode = models.AccessSocket
		dirty = true
	}
	if server.Role != models.RoleManager {
		server.Role = models.RoleManager
		dirty = true
	}
	if server.Name == "local" {
		server.Name = "manager"
		dirty = true
	}
	// Auto-stamp the manager's hostname from the Docker host (info.Name is the
	// real machine hostname, not the Miabi container's), so it needn't be
	// entered by hand. Non-destructive — only fills it when the admin left it
	// blank. Skipped silently if Docker is unreachable.
	if server.PublicHostname == "" {
		if info, ierr := s.docker.Info(ctx); ierr == nil {
			if hn := strings.TrimSpace(info.Name); hn != "" {
				server.PublicHostname = hn
				dirty = true
			}
		}
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

// NodeInput is the create/update payload for a remote node. The TLS key is
// plaintext on input and encrypted at rest.
type NodeInput struct {
	Name           string
	Address        string
	PublicIP       string
	PublicHostname string
	Connectivity   models.ServerConnectivity
	AccessMode     models.ServerAccessMode
	// DockerEndpoint is the host for non-agent modes (unix://… / tcp://host:2376).
	DockerEndpoint string
	TLSCACert      string // api, optional (PEM)
	TLSCert        string // api, optional (PEM)
	TLSKey         string // api, optional (PEM; stored encrypted)
	// Acknowledge confirms the caller accepts that a reachability change may
	// briefly interrupt the workloads running on the node. Required whenever the
	// update actually changes connectivity/access mode/endpoint/address/TLS.
	Acknowledge bool
}

// CreateNode registers a remote node. For agent mode it returns a one-time join
// token (only its hash is stored); other modes return an empty token.
func (s *Service) CreateNode(in NodeInput) (*models.Server, string, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, "", ErrNameRequired
	}
	if err := s.checkNodeLimit(); err != nil {
		return nil, "", err
	}
	mode := in.AccessMode
	if !models.ValidAccessMode(mode) {
		mode = models.AccessAgent
	}
	if in.Connectivity != models.ConnectivityEdgeGateway {
		in.Connectivity = models.ConnectivityPortForward
	}
	nodeSlug, err := slug.Unique(in.Name, "node", func(c string) (bool, error) {
		_, e := s.repo.FindBySlug(c)
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
		Name:           in.Name,
		Slug:           nodeSlug,
		Role:           models.RoleNode,
		Connectivity:   in.Connectivity,
		AccessMode:     mode,
		DockerEndpoint: strings.TrimSpace(in.DockerEndpoint),
		Address:        deriveAddress(mode, in.DockerEndpoint, in.Address),
		PublicIP:       strings.TrimSpace(in.PublicIP),
		PublicHostname: strings.TrimSpace(in.PublicHostname),
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
	return srv, token, nil
}

// UpdateNode edits a remote node's reachability settings. Credential fields are
// only replaced when non-empty (so an unchanged form keeps existing secrets).
func (s *Service) UpdateNode(id uint, in NodeInput) (*models.Server, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	// A change to how the control plane reaches this node — or how its workloads
	// are exposed — can briefly interrupt the apps and databases running on it.
	// Require an explicit acknowledgement so a plain metadata edit (which resends
	// the current reachability values unchanged) is never blocked, but a real
	// connectivity change from any client is.
	if reachabilityChanged(srv, in) && !in.Acknowledge {
		return nil, ErrConnectivityAckRequired
	}
	if srv.IsLocal {
		// The manager node's Docker access is fixed (always its local socket), but
		// its public DNS address and connectivity are editable: as the manager it
		// can run its own edge gateway or fall back to port-forward.
		srv.PublicIP = strings.TrimSpace(in.PublicIP)
		srv.PublicHostname = strings.TrimSpace(in.PublicHostname)
		if in.Connectivity == models.ConnectivityEdgeGateway || in.Connectivity == models.ConnectivityPortForward {
			srv.Connectivity = in.Connectivity
		}
		if err := s.repo.Update(srv); err != nil {
			return nil, err
		}
		return srv, nil
	}
	if strings.TrimSpace(in.Name) != "" {
		srv.Name = in.Name
	}
	if models.ValidAccessMode(in.AccessMode) {
		srv.AccessMode = in.AccessMode
	}
	if in.Connectivity == models.ConnectivityEdgeGateway || in.Connectivity == models.ConnectivityPortForward {
		srv.Connectivity = in.Connectivity
	}
	srv.DockerEndpoint = strings.TrimSpace(in.DockerEndpoint)
	srv.Address = deriveAddress(srv.AccessMode, in.DockerEndpoint, in.Address)
	srv.PublicIP = strings.TrimSpace(in.PublicIP)
	srv.PublicHostname = strings.TrimSpace(in.PublicHostname)
	if err := applyCredentials(srv, in); err != nil {
		return nil, err
	}
	if err := s.repo.Update(srv); err != nil {
		return nil, err
	}
	return srv, nil
}

// WorkloadImpact reports how many applications and database instances are placed
// on the node, so the UI can warn what a connectivity change would affect.
func (s *Service) WorkloadImpact(id uint) (apps int64, databases int64, err error) {
	return s.repo.CountWorkloads(id)
}

// FindBySwarmNodeID resolves the node backing a swarm node id.
func (s *Service) FindBySwarmNodeID(swarmNodeID string) (*models.Server, error) {
	return s.repo.FindBySwarmNodeID(swarmNodeID)
}

// RegisterClusterNode creates the record for a swarm worker that registered itself
// through the global agent service.
//
// The caller (cluster.AuthenticateAgent) has already verified the swarm node id is a
// member of THIS swarm, so this is not an open registration endpoint — the machine is
// one the swarm already trusts.
//
// The node is marked AutoJoined: the cluster brought it in, an admin did not. It has
// no join token of its own (it authenticates with the cluster token), and it defaults
// to port-forward connectivity like any other node — which in cluster mode is moot,
// since ingress reaches it over the overlay.
func (s *Service) RegisterClusterNode(swarmNodeID, hostname string) (*models.Server, error) {
	if err := s.checkNodeLimit(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(hostname)
	if name == "" {
		name = "node-" + shortID(swarmNodeID)
	}
	srv := &models.Server{
		Name:           uniqueName(s.repo, name),
		Slug:           slug.Make(name, shortID(swarmNodeID)),
		IsLocal:        false,
		Role:           models.RoleNode,
		AccessMode:     models.AccessAgent,
		Connectivity:   models.ConnectivityPortForward,
		Status:         models.ServerStatusUnknown,
		SwarmNodeID:    swarmNodeID,
		AutoJoined:     true,
		PublicHostname: strings.TrimSpace(hostname),
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
		taken[strings.ToLower(servers[i].Name)] = true
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

// LearnSwarmNodeID records the swarm node id the agent read from its own Docker
// engine at connect (X-Agent-Swarm-Node-ID).
//
// This is how a Miabi node gets mapped to its swarm node in every case, not just
// the one where Miabi did the joining. The control plane can only fill SwarmNodeID
// itself when it ran the `swarm join` (see cluster.JoinNode); a host joined with
// `docker swarm join`, or one that was already a member when Miabi met it, stayed
// unmapped forever. That is not cosmetic — an unmapped node cannot be resolved from
// a service's task, which is precisely what makes a replica's logs and metrics
// unreachable. The node is the authority on which node it is, so let it say.
//
// Unlike LearnEndpoint this OVERWRITES: a node can leave one swarm and join
// another, and its own report is always more current than ours. An empty value is
// ignored rather than treated as "left the swarm" — an older agent sends nothing at
// all, and we must not wipe a good id because of it. The leave paths clear it
// explicitly (cluster.LeaveNode).
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

// LearnEndpoint fills a node's public IP and/or hostname discovered from its
// agent connection, so an admin doesn't have to know them when adding the node:
// ip is the agent's source address as seen by the control plane, hostname is
// self-reported (X-Agent-Hostname). Non-destructive — it only fills fields the
// admin left blank, so an explicit value is never overwritten. Only a routable
// public IP is adopted (a private/NAT/loopback source would be misleading).
func (s *Service) LearnEndpoint(id uint, ip, hostname string) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return
	}
	changed := false
	if srv.PublicIP == "" {
		if pip := publicIP(ip); pip != "" {
			srv.PublicIP = pip
			changed = true
		}
	}
	if h := strings.TrimSpace(hostname); h != "" && srv.PublicHostname == "" {
		srv.PublicHostname = h
		changed = true
	}
	if changed {
		if err := s.repo.Update(srv); err != nil {
			logger.Warn("failed to persist learned node endpoint", "node", id, "error", err)
			return
		}
		logger.Info("learned node public endpoint from agent", "node", id, "public_ip", srv.PublicIP, "hostname", srv.PublicHostname)
	}
}

// publicIP returns raw as a normalized IP only when it is a routable public
// address; loopback/private/link-local/unspecified sources return "". raw may
// carry a port (RemoteAddr fallback), which is stripped.
func publicIP(raw string) string {
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

// deriveAddress resolves the reverse-proxy address for a node. For api the host
// is taken from the Docker endpoint (tcp://host:2376), so no separate address is
// asked for; other modes use the provided address.
// connectivityAckError signals that a reachability change needs the caller to
// acknowledge the impact. It carries a stable code so the API envelope and the
// web client can recognise it.
type connectivityAckError struct{}

func (*connectivityAckError) Error() string {
	return "changing node connectivity can interrupt running apps and databases; acknowledgement required"
}
func (*connectivityAckError) Code() string { return "CONNECTIVITY_ACK_REQUIRED" }

// validConnectivity reports whether c is one of the two known connectivity modes
// (blank/unknown values are ignored by UpdateNode, so they are not "changes").
func validConnectivity(c models.ServerConnectivity) bool {
	return c == models.ConnectivityEdgeGateway || c == models.ConnectivityPortForward
}

// reachabilityChanged reports whether in would actually alter how the control
// plane reaches the node or how its workloads are exposed (vs. a metadata-only
// edit that resends the current values). Mirrors what UpdateNode applies: for
// the local node only connectivity is mutable; for a remote node, access mode,
// endpoint, derived address and any newly supplied TLS material also count.
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

// applyCredentials sets/encrypts a node's TLS material from input. Every
// credential field is "blank = keep" so an edit form (which never receives the
// stored secrets back) does not wipe them; private keys are encrypted at rest.
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

// GatewayToken returns the node's edge-gateway provider token (decrypted),
// minting and persisting one (encrypted) on first use. Unlike the join token it
// is recoverable, so the gateway can be (re)deployed on demand.
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

// GatewayRedisPassword returns the per-node edge-gateway Redis password
// (decrypted), minting and persisting one (encrypted) on first use. Stable
// across redeploys so the gateway and its Redis keep matching credentials.
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
	srv.GatewayRedisPasswordEnc = enc
	if err := s.repo.Update(srv); err != nil {
		return "", err
	}
	return pw, nil
}

// SetGatewayUpdate persists the node's in-flight gateway update progress (nil
// clears it). Used by the safe-update flow so progress survives a reconnect.
func (s *Service) SetGatewayUpdate(id uint, p *models.GatewayUpdateProgress) error {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return ErrNodeNotFound
	}
	srv.GatewayUpdate = p
	return s.repo.Update(srv)
}

// SetGatewayConfig stores a node's custom edge-gateway config (empty resets to
// the rendered default). Returns the updated node.
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

// SetGatewayImage stores a node's edge-gateway image override (empty resets to
// the resolved default). Returns the updated node.
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

// MarkGatewayDeployed stamps the node's last successful gateway deploy time and
// clears any imported-gateway tracking (a fresh install supersedes an import).
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

// AdoptGateway tracks a pre-existing gateway container as this node's gateway
// (import). It records the container name + image, optionally copies the
// gateway's existing config into the node's stored config, switches the node to
// edge-gateway connectivity, and stamps the deploy time — without recreating
// anything, so the running gateway is untouched.
func (s *Service) AdoptGateway(id uint, container, image, configYAML string) (*models.Server, error) {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNodeNotFound
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

// ReleaseGateway stops tracking an imported gateway without touching the
// container (the inverse of AdoptGateway).
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

// AuthenticateProvider resolves the node a Goma HTTP-provider request belongs to,
// accepting either the agent join token or the node's gateway token. The slug in
// the URL must match the resolved node.
func (s *Service) AuthenticateProvider(slug, token string) (*models.Server, error) {
	srv, err := s.repo.FindBySlug(slug)
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

// Placeable validates that a node can accept a new resource (app/db/volume).
// The local node (0) is always placeable; a remote node must exist and not be
// cordoned. (Reachability is enforced separately when the operation runs.)
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

// SetSwarmNodeID records (or clears) the Docker Swarm node ID a node maps to,
// so the Nodes page can correlate it to `docker node ls`. Idempotent: a no-op
// when the value is unchanged, to avoid write churn on every cluster refresh.
func (s *Service) SetSwarmNodeID(id uint, swarmNodeID string) error {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return ErrNodeNotFound
	}
	if srv.SwarmNodeID == swarmNodeID {
		return nil
	}
	// Column-scoped: the row is also written on the same agent connect by
	// MarkConnected and LearnEndpoint, and a full Save from each would race them.
	return s.repo.UpdateSwarmNodeID(id, swarmNodeID)
}

// NameBySwarmNodeID resolves a swarm node id to a node's Miabi display name, so
// a cluster app's real replica placement can be shown by name. Returns "" when no
// node record carries that swarm id (the caller then falls back to a short id).
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
			return servers[i].Name
		}
	}
	return ""
}

// SetCordoned toggles a node's drain flag (no new placements when cordoned).
func (s *Service) SetCordoned(id uint, cordoned bool) error {
	srv, err := s.repo.FindByID(id)
	if err != nil {
		return ErrNodeNotFound
	}
	srv.Cordoned = cordoned
	return s.repo.Update(srv)
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
	return s.repo.Delete(srv.ID)
}

// Authenticate resolves the node an agent token belongs to (constant-time-ish
// via hash lookup).
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
