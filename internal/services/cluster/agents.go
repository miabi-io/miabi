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
	// AgentServiceName is the global service that carries the agent to every worker.
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
	// ErrNotSwarmMember is returned when an agent presents the cluster token but the
	// swarm node id it claims is not a member of THIS swarm. This is the check that
	// makes a shared token safe.
	ErrNotSwarmMember = errors.New("the agent's swarm node id is not a member of this cluster")
)

// TokenStore persists the cluster agent token's hash. Only the hash is kept: the
// plaintext lives in the service spec (where Docker already holds it) and nowhere
// else, so there is no second copy of a secret to leak.
type TokenStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
}

// NodeRegistrar creates or updates the Miabi node record an agent registers as.
// Satisfied by services/node.
type NodeRegistrar interface {
	FindBySwarmNodeID(swarmNodeID string) (*models.Server, error)
	RegisterClusterNode(swarmNodeID, hostname string) (*models.Server, error)
}

// SetAgentDeps wires what the global agent service needs (nil-safe; nil disables it).
func (s *Service) SetAgentDeps(tokens TokenStore, reg NodeRegistrar, controlURL string, images NetCheckImages, fallbackImage string) {
	s.tokens, s.registrar, s.controlURL = tokens, reg, controlURL
	s.agentImages, s.agentImageFallback = images, fallbackImage
}

const clusterAgentTokenKey = "cluster_agent_token_hash"

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
	// caCertEnv trusts a specific CA instead. Verification still happens — it is just
	// anchored on the operator's own authority — which is why it is strictly better
	// than skipping, and why the UI offers it first.
	caCertEnv = "MIABI_CA_CERT"
)

// AgentStatus reports whether the global agent service is deployed, how many of its
// tasks are up, and whether those agents are skipping TLS verification.
type AgentStatus struct {
	Deployed bool   `json:"deployed"`
	Running  int    `json:"running_tasks"`
	Image    string `json:"image,omitempty"`
	// InsecureTLS is true when the agents do NOT verify the control plane's certificate. It is surfaced so
	// a setting made once, to get a self-signed cert working, cannot quietly become permanent — an operator
	// should see that verification is off without reading a service spec.
	InsecureTLS bool `json:"insecure_tls"`
	// CustomCA is true when the agents verify against an operator-supplied CA. This is
	// the healthy state for a private control plane: verification still happens.
	CustomCA bool `json:"custom_ca"`
	// CACertPath is set when the CA comes from a file on the nodes rather than inline
	// PEM. Surfaced because it is a dependency on the host filesystem: the file must
	// exist on every node, including ones that join later.
	CACertPath string `json:"ca_cert_path,omitempty"`
}

// AgentStatus inspects the global agent service.
func (s *Service) AgentStatus(ctx context.Context) AgentStatus {
	if !s.CapCluster() {
		return AgentStatus{}
	}
	mgr := s.clients.Local()
	st, err := mgr.ServiceInspect(ctx, AgentServiceName)
	if err != nil {
		return AgentStatus{}
	}
	out := AgentStatus{Deployed: true, Running: int(st.RunningTasks), Image: st.Image}
	// The env also carries the token, so it is read here and discarded: only the
	// single fact leaves this function.
	if env, eerr := mgr.ServiceEnv(ctx, AgentServiceName); eerr == nil {
		for _, kv := range env {
			switch {
			case strings.EqualFold(kv, insecureEnv+"=true"):
				out.InsecureTLS = true
			case strings.HasPrefix(kv, caCertEnv+"=") && len(kv) > len(caCertEnv)+1:
				out.CustomCA = true
				// A path (rather than inline PEM) is worth surfacing separately: the agents then depend on that file existing
				// on every node, including ones that join later. Inline material arrives base64-encoded, so an absolute path
				// is the discriminator.
				if v := strings.TrimPrefix(kv, caCertEnv+"="); strings.HasPrefix(v, "/") {
					out.CACertPath = v
				}
			}
		}
	}
	return out
}

// DeployAgents installs the Miabi agent on every swarm worker as a global service. Deliberately an
// explicit admin action rather than something Enable does silently: it grants Miabi the Docker socket —
// root-equivalent — on every machine that is now, or ever becomes, a member of this swarm.
type AgentOptions struct {
	InsecureTLS bool
	// CACert is the PEM itself, shipped to the agents in their environment.
	CACert string
	// CACertPath is a CA file that already exists ON EVERY NODE, typically the host's own trust anchor. It
	// is why agents fail while hosts are fine: the agent container has its own stock bundle. Bind-mounting
	// the file the host already has stays correct when the CA is rotated.
	CACertPath string
}

// InsecureTLS skips verification of the control plane's certificate, for a control plane behind a
// self-signed or private CA where an agent would otherwise never connect. It is a genuine downgrade: an
// interceptor could impersonate a control plane that drives Docker on every node.
func (s *Service) DeployAgents(ctx context.Context, opts AgentOptions) error {
	if !s.CapCluster() {
		return ErrNotEnabled
	}
	if s.tokens == nil || s.registrar == nil {
		return errors.New("the cluster agent service is not wired")
	}
	if strings.TrimSpace(s.controlURL) == "" {
		return ErrControlURLRequired
	}
	image := s.agentImage()
	if strings.TrimSpace(image) == "" {
		return ErrAgentImageRequired
	}

	// Mint a fresh token and store only its hash. The plaintext goes into the service spec, which Docker
	// already holds — keeping a second copy would be a second thing to leak. Rotating on every deploy means a
	// token that ever escaped stops working the next time an admin redeploys.
	token := clusterTokenPrefix + randHex(32)
	if err := s.tokens.Set(clusterAgentTokenKey, hashClusterToken(token)); err != nil {
		return fmt.Errorf("store the cluster agent token: %w", err)
	}

	env := []string{
		"MIABI_CONTROL_URL=" + strings.TrimRight(s.controlURL, "/"),
		"MIABI_NODE_TOKEN=" + token,
	}
	// A CA and skip-verify are mutually exclusive in effect (the agent warns if both
	// arrive), so send only what was chosen.
	binds := []docker.ServiceBind{{Source: dockerSock, Target: dockerSock}}
	switch {
	case opts.InsecureTLS:
		env = append(env, insecureEnv+"=true")
		logger.Warn("deploying cluster agents WITHOUT control-plane certificate verification",
			"control_url", s.controlURL)
	case strings.TrimSpace(opts.CACertPath) != "":
		// Mount the CA the hosts already trust into the container, which does not. The
		// agent reads MIABI_CA_CERT as a path when it is not PEM.
		path := strings.TrimSpace(opts.CACertPath)
		binds = append(binds, docker.ServiceBind{Source: path, Target: path, ReadOnly: true})
		env = append(env, caCertEnv+"="+path)
		logger.Info("deploying cluster agents with a host CA file", "path", path, "control_url", s.controlURL)
	case strings.TrimSpace(opts.CACert) != "":
		// Base64, not raw PEM. A certificate is multi-line and an environment variable is a poor place for
		// newlines — they survive some transports and not others, and a PEM whose line breaks were eaten is not a
		// PEM at all. One flat token cannot be mangled. The agent decodes it.
		env = append(env, caCertEnv+"="+base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(opts.CACert))))
		logger.Info("deploying cluster agents with a custom certificate authority",
			"control_url", s.controlURL)
	}

	spec := docker.ServiceSpec{
		Name:   AgentServiceName,
		Image:  image,
		Global: true,
		// The manager already has a direct socket client; it needs no agent, and giving
		// it one would have it register a second record for itself.
		Constraints: []string{"node.role==worker"},
		Binds:       binds,
		Env:         env,
		// Platform identity, so an agent's task container is recognized as part of Miabi on the worker it lands on
		// — a node whose engine the control plane can only reach THROUGH that agent. Keeps ManagedLabel for the
		// existing call sites that scope raw services by it.
		Labels: docker.PlatformLabels(docker.RoleAgent, docker.ManagedByMiabi,
			map[string]string{docker.ManagedLabel: "true"}),
	}

	mgr := s.clients.Local()
	if _, err := mgr.ServiceInspect(ctx, AgentServiceName); err == nil {
		if err := mgr.ServiceUpdate(ctx, AgentServiceName, spec); err != nil {
			return fmt.Errorf("update the agent service: %w", err)
		}
		logger.Info("cluster agent service updated", "image", image)
		return nil
	}
	if _, err := mgr.ServiceCreate(ctx, spec); err != nil {
		return fmt.Errorf("create the agent service: %w", err)
	}
	logger.Info("cluster agent service deployed to every worker", "image", image)
	return nil
}

// RemoveAgents tears the global agent service down. The node records stay — their
// history, labels and placements are still meaningful — they simply go back to being
// unmanaged (no metrics, stats or shell), which the Nodes page already says plainly.
func (s *Service) RemoveAgents(ctx context.Context) error {
	if !s.CapCluster() {
		return ErrNotEnabled
	}
	if err := s.clients.Local().ServiceRemove(ctx, AgentServiceName); err != nil {
		return err
	}
	// Invalidate the token with it: an agent container that outlives the service (a
	// stale task, a hand-run copy) must not keep a working credential.
	if s.tokens != nil {
		_ = s.tokens.Set(clusterAgentTokenKey, "")
	}
	logger.Info("cluster agent service removed")
	return nil
}

// AuthenticateAgent authorizes an agent that presented the CLUSTER token and returns the Miabi node it
// is. The token alone proves nothing — every agent carries the same one — so identity comes from the
// swarm node id the agent read off its engine, trusted only because the manager can verify membership.
func (s *Service) AuthenticateAgent(ctx context.Context, token, swarmNodeID, hostname string) (*models.Server, error) {
	if s.tokens == nil || s.registrar == nil {
		return nil, ErrBadClusterToken
	}
	want, err := s.tokens.Get(clusterAgentTokenKey)
	if err != nil || strings.TrimSpace(want) == "" {
		return nil, ErrBadClusterToken // no agent service deployed
	}
	if subtle.ConstantTimeCompare([]byte(hashClusterToken(token)), []byte(want)) != 1 {
		return nil, ErrBadClusterToken
	}
	swarmNodeID = strings.TrimSpace(swarmNodeID)
	if swarmNodeID == "" {
		return nil, ErrNotSwarmMember // an agent that cannot say who it is cannot register
	}
	if !s.isSwarmMember(ctx, swarmNodeID) {
		return nil, ErrNotSwarmMember
	}
	// Known node → reuse it. Unknown → this is a worker the swarm brought in.
	if srv, err := s.registrar.FindBySwarmNodeID(swarmNodeID); err == nil {
		return srv, nil
	}
	return s.registrar.RegisterClusterNode(swarmNodeID, hostname)
}

// ErrBadClusterToken is returned when the presented cluster agent token is unknown.
var ErrBadClusterToken = errors.New("invalid cluster agent token")

// isSwarmMember checks the claimed id against the manager's own membership list. This
// is the whole security of a shared token: it authorizes registration, but only for a
// machine the swarm already trusts.
func (s *Service) isSwarmMember(ctx context.Context, swarmNodeID string) bool {
	nodes, err := s.clients.Local().SwarmNodes(ctx)
	if err != nil {
		return false
	}
	for _, n := range nodes {
		if n.ID == swarmNodeID {
			return true
		}
	}
	return false
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
