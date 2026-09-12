// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// A swarm member with no Miabi agent runs tasks perfectly well but Miabi holds no Docker client for it,
// so an app scheduled there has no metrics, stats or shell. Swarm can fix this itself: a GLOBAL service
// runs one task on every node the constraints allow, including nodes that join later.
const (
	// AgentServiceName is the global service that carries the agent to every swarm member.
	AgentServiceName = "miabi-agent"
	// dockerSock is bind-mounted into each agent task; it is the agent's whole job.
	dockerSock = "/var/run/docker.sock"
	// clusterTokenPrefix marks a cluster-wide agent token, distinct from the per-node
	// mbn_ tokens an admin hands to install-agent.sh.
	clusterTokenPrefix = "mbc_"
)

var (
	// ErrControlURLRequired is returned when the control plane has no address that
	// workers could dial. Without it the agents would start and never connect.
	ErrControlURLRequired = errors.New("MIABI_CONTROL_URL must be set: it is the address the agents dial back on")
	// ErrAgentImageRequired is returned when no agent image is configured.
	ErrAgentImageRequired = errors.New("no agent image is configured")
	// ErrNotSwarmMember is returned when an agent presents a cluster token but the swarm node id it claims
	// is not a member of that cluster's swarm. This is the check that makes a shared token safe.
	ErrNotSwarmMember = errors.New("the agent's swarm node id is not a member of this cluster")
	// ErrBadClusterToken is returned when the presented cluster agent token is unknown.
	ErrBadClusterToken = errors.New("invalid cluster agent token")
)

// NodeRegistrar creates or updates the Miabi node record an agent registers as.
// Satisfied by services/node.
type NodeRegistrar interface {
	FindBySwarmNodeID(swarmNodeID string) (*models.Server, error)
	RegisterClusterNode(clusterID uint, swarmNodeID, hostname string) (*models.Server, error)
}

// SetAgentDeps wires what the global agent service needs (nil-safe; nil disables it).
func (s *Service) SetAgentDeps(reg NodeRegistrar, controlURL string, images NetCheckImages, fallbackImage string) {
	s.registrar, s.controlURL = reg, controlURL
	s.agentImages, s.agentImageFallback = images, fallbackImage
}

func (s *Service) agentImage() string {
	if s.agentImages != nil {
		if r := s.agentImages.Ref("agent"); r != "" {
			return r
		}
	}
	return s.agentImageFallback
}

const (
	// insecureEnv is the agent's opt-out of certificate verification entirely.
	insecureEnv = "MIABI_AGENT_INSECURE_SKIP_VERIFY"
	// caCertEnv trusts a specific CA instead. Verification still happens, anchored on the
	// operator's own authority, which is why the UI offers it before skipping.
	caCertEnv = "MIABI_CA_CERT"
)

// AgentStatus reports whether the global agent service is deployed, how many of its
// tasks are up, and whether those agents are skipping TLS verification.
type AgentStatus struct {
	Deployed bool   `json:"deployed"`
	Running  int    `json:"running_tasks"`
	Image    string `json:"image,omitempty"`
	// InsecureTLS is true when the agents do NOT verify the control plane's certificate, surfaced so a
	// one-off workaround for a self-signed cert cannot quietly become permanent.
	InsecureTLS bool `json:"insecure_tls"`
	// CustomCA is true when the agents verify against an operator-supplied CA.
	CustomCA bool `json:"custom_ca"`
	// CACertPath is set when the CA comes from a file that must exist on every node.
	CACertPath string `json:"ca_cert_path,omitempty"`
}

// AgentStatus inspects a cluster's global agent service.
func (s *Service) AgentStatus(ctx context.Context, clusterID uint) AgentStatus {
	if !s.IsSwarm(clusterID) {
		return AgentStatus{}
	}
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return AgentStatus{}
	}
	st, err := mgr.ServiceInspect(ctx, AgentServiceName)
	if err != nil {
		return AgentStatus{}
	}
	out := AgentStatus{Deployed: true, Running: int(st.RunningTasks), Image: st.Image}
	// The env also carries the token, so it is read here and discarded.
	if env, eerr := mgr.ServiceEnv(ctx, AgentServiceName); eerr == nil {
		for _, kv := range env {
			switch {
			case strings.EqualFold(kv, insecureEnv+"=true"):
				out.InsecureTLS = true
			case strings.HasPrefix(kv, caCertEnv+"=") && len(kv) > len(caCertEnv)+1:
				out.CustomCA = true
				// Inline material arrives base64-encoded, so an absolute path is the discriminator.
				if v := strings.TrimPrefix(kv, caCertEnv+"="); strings.HasPrefix(v, "/") {
					out.CACertPath = v
				}
			}
		}
	}
	return out
}

// AgentOptions configures how the agents trust the control plane's certificate.
type AgentOptions struct {
	InsecureTLS bool
	// CACert is the PEM itself, shipped to the agents in their environment.
	CACert string
	// CACertPath is a CA file that already exists on every node, bind-mounted into each agent. The agent
	// container has its own stock bundle, which is why agents fail while hosts are fine.
	CACertPath string
}

// DeployAgents installs the Miabi agent on every node of a cluster's swarm as a global service. It is an
// explicit admin action because it grants Miabi the Docker socket, root-equivalent, on every machine that
// is now, or ever becomes, a member of this swarm.
func (s *Service) DeployAgents(ctx context.Context, clusterID uint, opts AgentOptions) error {
	if !s.IsSwarm(clusterID) {
		return ErrNotEnabled
	}
	if s.store == nil || s.registrar == nil {
		return errors.New("the cluster agent service is not wired")
	}
	if strings.TrimSpace(s.controlURL) == "" {
		return ErrControlURLRequired
	}
	image := s.agentImage()
	if strings.TrimSpace(image) == "" {
		return ErrAgentImageRequired
	}
	c, err := s.find(clusterID)
	if err != nil {
		return err
	}
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return err
	}

	// Store only the hash: the plaintext lives in the service spec, which Docker already holds. Rotating on
	// every deploy means a token that ever escaped stops working at the next redeploy.
	token := clusterTokenPrefix + randHex(32)
	if err := s.store.UpdateColumns(c.ID, map[string]any{"agent_token_hash": hashClusterToken(token)}); err != nil {
		return fmt.Errorf("store the cluster agent token: %w", err)
	}

	env := []string{
		"MIABI_CONTROL_URL=" + strings.TrimRight(s.controlURL, "/"),
		"MIABI_NODE_TOKEN=" + token,
	}
	binds := []docker.ServiceBind{{Source: dockerSock, Target: dockerSock}}
	switch {
	case opts.InsecureTLS:
		env = append(env, insecureEnv+"=true")
		logger.Warn("deploying cluster agents WITHOUT control-plane certificate verification",
			"cluster", c.Name, "control_url", s.controlURL)
	case strings.TrimSpace(opts.CACertPath) != "":
		path := strings.TrimSpace(opts.CACertPath)
		binds = append(binds, docker.ServiceBind{Source: path, Target: path, ReadOnly: true})
		env = append(env, caCertEnv+"="+path)
		logger.Info("deploying cluster agents with a host CA file", "cluster", c.Name, "path", path)
	case strings.TrimSpace(opts.CACert) != "":
		// Base64, not raw PEM: newlines in an environment variable survive some transports and not others.
		env = append(env, caCertEnv+"="+base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(opts.CACert))))
		logger.Info("deploying cluster agents with a custom certificate authority", "cluster", c.Name)
	}

	s.labelDirectNodes(ctx, mgr, clusterID)
	spec := docker.ServiceSpec{
		Name:        AgentServiceName,
		Image:       image,
		Global:      true,
		Constraints: []string{"node.labels." + AgentLabel + "!=" + agentLabelDirect},
		Binds:       binds,
		Env:         env,
		Labels: docker.PlatformLabels(docker.RoleAgent, docker.ManagedByMiabi,
			map[string]string{docker.ManagedLabel: "true"}),
	}

	if _, err := mgr.ServiceInspect(ctx, AgentServiceName); err == nil {
		if err := mgr.ServiceUpdate(ctx, AgentServiceName, spec); err != nil {
			return fmt.Errorf("update the agent service: %w", err)
		}
		logger.Info("cluster agent service updated", "cluster", c.Name, "image", image)
		return nil
	}
	if _, err := mgr.ServiceCreate(ctx, spec); err != nil {
		return fmt.Errorf("create the agent service: %w", err)
	}
	logger.Info("cluster agent service deployed", "cluster", c.Name, "image", image)
	return nil
}

// labelDirectNodes marks the cluster's nodes Miabi already reaches directly: the local socket, or an agent
// an admin installed. Nodes the agent service registered itself stay unlabelled, so it keeps running there.
func (s *Service) labelDirectNodes(ctx context.Context, mgr docker.Client, clusterID uint) {
	servers, err := s.nodes.List(ctx)
	if err != nil {
		return
	}
	for i := range servers {
		srv := &servers[i]
		if srv.AutoJoined || srv.SwarmNodeID == "" || !s.inCluster(srv, clusterID) {
			continue
		}
		labelDirect(ctx, mgr, srv.SwarmNodeID)
	}
}

// RemoveAgents tears a cluster's global agent service down. The node records stay; those nodes simply go
// back to being unmanaged.
func (s *Service) RemoveAgents(ctx context.Context, clusterID uint) error {
	if !s.IsSwarm(clusterID) {
		return ErrNotEnabled
	}
	mgr, err := s.Manager(ctx, clusterID)
	if err != nil {
		return err
	}
	if err := mgr.ServiceRemove(ctx, AgentServiceName); err != nil {
		return err
	}
	// An agent container that outlives the service must not keep a working credential.
	if c, ferr := s.find(clusterID); ferr == nil && s.store != nil {
		_ = s.store.UpdateColumns(c.ID, map[string]any{"agent_token_hash": ""})
	}
	logger.Info("cluster agent service removed", "cluster", clusterID)
	return nil
}

// AuthenticateAgent authorizes an agent that presented a cluster token and returns the Miabi node it is.
// The token only names the cluster; identity comes from the swarm node id the agent read off its engine,
// trusted only because that cluster's manager confirms the membership.
func (s *Service) AuthenticateAgent(ctx context.Context, token, swarmNodeID, hostname string) (*models.Server, error) {
	if s.store == nil || s.registrar == nil {
		return nil, ErrBadClusterToken
	}
	hash := hashClusterToken(token)
	c, err := s.store.FindByAgentTokenHash(hash)
	if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(c.AgentTokenHash)) != 1 {
		return nil, ErrBadClusterToken
	}
	swarmNodeID = strings.TrimSpace(swarmNodeID)
	if swarmNodeID == "" {
		return nil, ErrNotSwarmMember
	}
	mgr, err := s.Manager(ctx, c.ID)
	if err != nil || !isMember(ctx, mgr, swarmNodeID) {
		return nil, ErrNotSwarmMember
	}
	if srv, err := s.registrar.FindBySwarmNodeID(swarmNodeID); err == nil {
		return srv, nil
	}
	return s.registrar.RegisterClusterNode(c.ID, swarmNodeID, hostname)
}

func hashClusterToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
