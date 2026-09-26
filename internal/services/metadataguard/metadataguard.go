// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package metadataguard keeps containers off the cloud metadata service on every node.
//
// Each node runs a small helper container (host network, NET_ADMIN, the host filesystem read-only)
// that applies an nftables table with the host's own nft and re-applies it if it disappears. Its
// restart policy brings it back after a reboot, so the control plane only has to make sure it exists.
package metadataguard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/platformimage"
)

const (
	// ContainerName is the helper's name on every node.
	ContainerName = "mb-metadata-guard"
	// Table is the nftables table the helper owns. Deleting it is the rollback.
	Table = "miabi_metadata_guard"

	defaultImage = "busybox:1.36"
	opTimeout    = 2 * time.Minute
	concurrency  = 8
)

// blockedV4 are the metadata endpoints of the clouds Miabi is commonly run on: link-local (AWS, GCP,
// Azure IMDS, Oracle, Hetzner, DigitalOcean, OpenStack), Azure's wireserver and Alibaba Cloud.
var blockedV4 = []string{"169.254.0.0/16", "168.63.129.16", "100.100.100.200"}

// blockedV6 is AWS's IPv6 IMDS endpoint.
var blockedV6 = []string{"fd00:ec2::254"}

// bridgeInterfaces are where container traffic enters the host: the default bridge, every user
// network's br-<id>, and swarm's gateway bridge.
var bridgeInterfaces = []string{"docker0", "br-*", "docker_gwbridge"}

// Ruleset renders the table. DNS stays open because GCP and Azure resolve through these addresses,
// and a container on the default bridge inherits the host's resolver.
func Ruleset() string {
	var b strings.Builder
	// Declaring then deleting makes the load an atomic replace that also works on first install.
	fmt.Fprintf(&b, "table inet %s {}\ndelete table inet %s\ntable inet %s {\n", Table, Table, Table)
	fmt.Fprintf(&b, "  set metadata4 { type ipv4_addr; flags interval; elements = { %s } }\n", strings.Join(blockedV4, ", "))
	fmt.Fprintf(&b, "  set metadata6 { type ipv6_addr; elements = { %s } }\n", strings.Join(blockedV6, ", "))
	b.WriteString("  chain forward {\n    type filter hook forward priority filter - 1; policy accept;\n")
	for _, iface := range bridgeInterfaces {
		fmt.Fprintf(&b, "    iifname %q ip daddr @metadata4 meta l4proto { tcp, udp } th dport 53 accept\n", iface)
		fmt.Fprintf(&b, "    iifname %q ip daddr @metadata4 counter drop\n", iface)
		fmt.Fprintf(&b, "    iifname %q ip6 daddr @metadata6 counter drop\n", iface)
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

// guardScript runs in the helper. It validates before applying so a bad ruleset never replaces a
// good one, and checks every minute that the table is still there. A host without nft is reported
// and left alone rather than restarted in a loop.
const guardScript = `set -u
echo "$MB_NFT_RULES" | base64 -d > /tmp/rules.nft
nft() { chroot /host nft "$@"; }
if ! nft --version >/dev/null 2>&1; then
  echo "metadata guard: nft is not installed on this host; containers can still reach the cloud metadata service" >&2
  while :; do sleep 3600; done
fi
apply() {
  if nft -c -f /dev/stdin < /tmp/rules.nft && nft -f /dev/stdin < /tmp/rules.nft; then
    echo "metadata guard: table inet ` + Table + ` applied"
  else
    echo "metadata guard: failed to apply the ruleset" >&2
  fi
}
apply
while sleep 60; do
  nft list table inet ` + Table + ` >/dev/null 2>&1 || apply
done
`

// NodeDocker resolves the Docker client for a node id.
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
}

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// Servers lists the nodes to guard.
type Servers interface {
	List() ([]models.Server, error)
}

// Service installs, keeps and removes the guard on nodes.
type Service struct {
	clients NodeDocker
	servers Servers
	images  ImageResolver
	enabled bool
}

// NewService returns a guard service. enabled=false removes the guard from every node it finds it on
// (MIABI_METADATA_GUARD=false).
func NewService(clients NodeDocker, servers Servers, enabled bool) *Service {
	return &Service{clients: clients, servers: servers, enabled: enabled}
}

// SetImageResolver wires the deployment-config resolver for the helper image.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

func (s *Service) image() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyHelper); r != "" {
			return r
		}
	}
	return defaultImage
}

func (s *Service) spec() docker.RunSpec {
	spec := docker.RunSpec{
		Name:          ContainerName,
		Image:         s.image(),
		Entrypoint:    []string{"/bin/sh", "-c"},
		Cmd:           []string{guardScript},
		Env:           []string{"MB_NFT_RULES=" + base64.StdEncoding.EncodeToString([]byte(Ruleset()))},
		NetworkMode:   "host",
		CapDrop:       []string{"ALL"},
		CapAdd:        []string{"NET_ADMIN", "SYS_CHROOT"},
		Binds:         []docker.BindMount{{Source: "/", Target: "/host", ReadOnly: true}},
		RestartPolicy: "always",
		MemoryBytes:   32 << 20,
		Labels: map[string]string{
			docker.LabelRole: docker.RoleMetadataGuard,
		},
	}
	spec.Labels[docker.LabelSpecHash] = specHash(spec)
	return spec
}

func specHash(spec docker.RunSpec) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%q\x00%q\x00%q\x00%q\x00%v", spec.Image, spec.Entrypoint, spec.Cmd, spec.Env, spec.CapAdd, spec.Binds)
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// Ensure makes the node's guard match what this build would deploy. A running guard with the same
// spec is left alone, so this is cheap to call on every reconnect and sweep.
func (s *Service) Ensure(ctx context.Context, dc docker.Client, node string) error {
	if !s.enabled {
		return s.Remove(ctx, dc, node)
	}
	want := s.spec()
	if cur, err := dc.InspectContainer(ctx, ContainerName); err == nil &&
		cur.Labels[docker.LabelSpecHash] == want.Labels[docker.LabelSpecHash] && cur.State == "running" {
		return nil
	}
	if present, err := dc.ImageExists(ctx, want.Image); err != nil || !present {
		if err := dc.PullImage(ctx, want.Image, nil); err != nil {
			return fmt.Errorf("pull %s: %w", want.Image, err)
		}
	}
	_ = dc.RemoveContainer(ctx, ContainerName, true)
	if _, err := dc.RunContainer(ctx, want); err != nil {
		return fmt.Errorf("run metadata guard: %w", err)
	}
	logger.Info("metadata guard deployed", "node", node)
	return nil
}

// Remove takes the guard off a node and deletes its table. Stopping the helper alone leaves the
// rules in place, which is the safe default for every other reason a container stops.
func (s *Service) Remove(ctx context.Context, dc docker.Client, node string) error {
	if _, err := dc.InspectContainer(ctx, ContainerName); err != nil {
		return nil
	}
	if err := dc.RemoveContainer(ctx, ContainerName, true); err != nil {
		return err
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:        fmt.Sprintf("mb-metadata-guard-off-%d", time.Now().UnixNano()),
		Image:       s.image(),
		Entrypoint:  []string{"/bin/sh", "-c"},
		Cmd:         []string{"chroot /host nft delete table inet " + Table + " 2>/dev/null; true"},
		NetworkMode: "host",
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{"NET_ADMIN", "SYS_CHROOT"},
		Binds:       []docker.BindMount{{Source: "/", Target: "/host", ReadOnly: true}},
	})
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("remove metadata guard table: exited %d: %s", exit, out)
	}
	logger.Info("metadata guard removed", "node", node)
	return nil
}

// OnConnect is the node manager's agent-connect hook: an agent that reconnects after a reboot gets
// its guard checked straight away rather than at the next sweep.
func (s *Service) OnConnect(ctx context.Context, srv *models.Server, _ string, dc docker.Client) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	if err := s.Ensure(ctx, dc, srv.Name); err != nil {
		logger.Warn("metadata guard: could not deploy", "node", srv.Name, "error", err)
	}
}

// Sweep ensures the guard on every reachable node, the manager included.
func (s *Service) Sweep(ctx context.Context) error {
	list, err := s.servers.List()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i := range list {
		srv := list[i]
		if !srv.IsLocal && srv.Status == models.ServerStatusOffline {
			continue
		}
		dc, err := s.clients.For(srv.ID)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			octx, cancel := context.WithTimeout(ctx, opTimeout)
			defer cancel()
			if err := s.Ensure(octx, dc, srv.Name); err != nil {
				logger.Warn("metadata guard: could not deploy", "node", srv.Name, "error", err)
			}
		}()
	}
	wg.Wait()
	return nil
}
