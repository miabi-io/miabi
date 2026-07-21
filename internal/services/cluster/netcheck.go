// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

// Cluster networking fails in a way that is almost impossible to read from the
// outside: the swarm forms, DNS resolves, and then packets vanish. An app comes up,
// resolves its database to a plausible overlay IP, and hangs — which looks like a
// broken app, not a broken network.
//
// NetCheck makes that visible. It probes the real data plane, on the real overlay,
// from every node to every other node, and separates the three failures that look
// identical from the app's side:
//
//	DNS      — does the name resolve on the source node? (gossip, 7946)
//	TCP      — does a connection complete? (VXLAN 4789/udp, and ESP if encrypted)
//	Payload  — does a 1400-byte body survive? (MTU black hole)
//
// The payload check is the one nobody thinks to run, and it is the only thing that
// catches an MTU black hole — where handshakes succeed and every large response
// hangs forever.
const (
	probePort     = 9099
	probePayload  = 1400 // bytes; large enough to exceed a broken overlay MTU
	probeNameBase = "mb-netcheck"
	probeTimeout  = 90 * time.Second
)

// NetCheckProbe is one node's participation in the check.
type NetCheckProbe struct {
	ServerID   uint   `json:"server_id"`
	NodeName   string `json:"node_name"`
	Reachable  bool   `json:"reachable"` // Miabi could start a probe here
	Error      string `json:"error,omitempty"`
	OverlayIP  string `json:"overlay_ip,omitempty"`
	OverlayMTU int    `json:"overlay_mtu,omitempty"`
}

// NetCheckResult is one directed test: can `from` reach `to` over the overlay?
type NetCheckResult struct {
	From    string `json:"from"`
	To      string `json:"to"`
	DNS     bool   `json:"dns"`     // the name resolved on the source node
	TCP     bool   `json:"tcp"`     // a connection completed
	Payload bool   `json:"payload"` // a 1400-byte body round-tripped
	IP      string `json:"ip,omitempty"`
	Error   string `json:"error,omitempty"`
	// Verdict names the failure in the operator's terms rather than making them
	// infer it from three booleans.
	Verdict string `json:"verdict"`
}

// NetCheck is the whole report.
type NetCheck struct {
	Network string           `json:"network"`
	Probes  []NetCheckProbe  `json:"probes"`
	Results []NetCheckResult `json:"results"`
	OK      bool             `json:"ok"`
	Summary string           `json:"summary"`
}

// NetCheckImages resolves the probe image (a socat/alpine utility image).
type NetCheckImages interface {
	Ref(key string) string
}

// SetNetCheckImage wires the image the probe containers run.
func (s *Service) SetNetCheckImage(images NetCheckImages, fallback string) {
	s.probeImages, s.probeImageFallback = images, fallback
}

func (s *Service) probeImage() string {
	if s.probeImages != nil {
		if r := s.probeImages.Ref("util.relay"); r != "" {
			return r
		}
	}
	return s.probeImageFallback
}

// NetCheck probes the cluster's overlay data plane from every node to every other
// node. It requires cluster mode, runs only on nodes Miabi has a Docker client for
// (it must start a container there), and cleans up after itself.
func (s *Service) NetCheck(ctx context.Context) (NetCheck, error) {
	if !s.CapCluster() {
		return NetCheck{}, ErrNotEnabled
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	overlay := node.IngressOverlay // every cluster node joins it; nothing new to create
	out := NetCheck{Network: overlay}

	// Only nodes with a Docker client can host a probe. An unmanaged swarm member
	// cannot be probed at all — say so rather than silently omitting it.
	servers, err := s.nodes.List(ctx)
	if err != nil {
		return NetCheck{}, err
	}
	image := s.probeImage()

	type target struct {
		srv   models.Server
		dc    docker.Client
		alias string
	}
	var targets []target
	for i := range servers {
		srv := servers[i]
		p := NetCheckProbe{ServerID: srv.ID, NodeName: srv.Name}
		dc, derr := s.clients.For(srv.ID)
		if derr != nil {
			p.Error = "no Docker connection to this node (offline, or a swarm member with no Miabi agent)"
			out.Probes = append(out.Probes, p)
			continue
		}
		alias := fmt.Sprintf("%s-%d", probeNameBase, srv.ID)
		if err := s.startProbe(ctx, dc, image, overlay, alias); err != nil {
			p.Error = err.Error()
			out.Probes = append(out.Probes, p)
			continue
		}
		defer s.stopProbe(dc, alias)
		p.Reachable = true
		out.Probes = append(out.Probes, p)
		targets = append(targets, target{srv: srv, dc: dc, alias: alias})
	}
	if len(targets) < 2 {
		out.OK = len(targets) == 1
		out.Summary = "Need at least two nodes with a Miabi agent to test cross-node connectivity."
		return out, nil
	}

	// Give the probes a moment to bind and their names to propagate through the
	// swarm's gossip, so a DNS miss means a real problem rather than a race.
	select {
	case <-ctx.Done():
		return out, ctx.Err()
	case <-time.After(3 * time.Second):
	}

	// Probe every ordered pair concurrently: connectivity is not symmetric (a
	// one-way firewall rule is common), so A->B and B->A are separate facts.
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, from := range targets {
		for _, to := range targets {
			if from.srv.ID == to.srv.ID {
				continue
			}
			wg.Add(1)
			go func(from, to target) {
				defer wg.Done()
				r := s.probePair(ctx, from.dc, image, overlay, from.srv.Name, to.srv.Name, to.alias)
				mu.Lock()
				out.Results = append(out.Results, r)
				mu.Unlock()
			}(from, to)
		}
	}
	wg.Wait()

	out.OK, out.Summary = summarize(out.Results)
	return out, nil
}

// startProbe runs an echo server on the overlay, reachable by its alias.
func (s *Service) startProbe(ctx context.Context, dc docker.Client, image, overlay, alias string) error {
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull probe image: %w", err)
	}
	_ = dc.RemoveContainer(ctx, alias, true) // a leftover from an interrupted run
	_, err := dc.RunContainer(ctx, docker.RunSpec{
		Name:           alias,
		Image:          image,
		Networks:       []string{overlay},
		NetworkAliases: []string{alias},
		Labels:         map[string]string{docker.ManagedLabel: "true"},
		// The image's entrypoint is socat; echo whatever is sent back to the sender.
		Cmd: []string{fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", probePort), "EXEC:/bin/cat"},
	})
	if err != nil {
		return fmt.Errorf("start probe: %w", err)
	}
	return nil
}

func (s *Service) stopProbe(dc docker.Client, alias string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := dc.RemoveContainer(ctx, alias, true); err != nil {
		logger.Warn("net check: could not remove a probe container", "container", alias, "error", err)
	}
}

// probePair runs a one-shot on the source node that resolves, dials and pushes a
// 1400-byte payload to the target's alias — the three failures an app cannot tell
// apart from the inside.
func (s *Service) probePair(ctx context.Context, dc docker.Client, image, overlay, fromName, toName, toAlias string) NetCheckResult {
	r := NetCheckResult{From: fromName, To: toName}

	script := fmt.Sprintf(`
ip=$(getent hosts %[1]s 2>/dev/null | awk 'NR==1{print $1}')
[ -z "$ip" ] && ip=$(nslookup %[1]s 2>/dev/null | awk '/^Address/ && $NF !~ /#/ {print $NF; exit}')
[ -n "$ip" ] && echo "DNS=ok IP=$ip" || { echo "DNS=fail"; exit 0; }
nc -z -w 5 %[1]s %[2]d 2>/dev/null && echo "TCP=ok" || { echo "TCP=fail"; exit 0; }
n=$(head -c %[3]d /dev/zero | tr '\0' 'x' | nc -w 8 %[1]s %[2]d 2>/dev/null | wc -c)
[ "$n" -ge %[3]d ] && echo "PAYLOAD=ok" || echo "PAYLOAD=fail GOT=$n"
`, toAlias, probePort, probePayload)

	_, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:       fmt.Sprintf("%s-c-%s", probeNameBase, randSuffix()),
		Image:      image,
		Networks:   []string{overlay},
		Labels:     map[string]string{docker.ManagedLabel: "true"},
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{script},
	})
	if err != nil {
		r.Error = err.Error()
		r.Verdict = "could not run the probe on " + fromName
		return r
	}

	r.DNS = strings.Contains(out, "DNS=ok")
	r.TCP = strings.Contains(out, "TCP=ok")
	r.Payload = strings.Contains(out, "PAYLOAD=ok")
	if i := strings.Index(out, "IP="); i >= 0 {
		r.IP = strings.Fields(out[i+3:])[0]
	}
	r.Verdict = verdict(r)
	return r
}

// verdict names the failure. Each of these looks identical from inside an app — a
// name that resolves and a connection that never completes — so the whole point of
// the check is to tell them apart.
func verdict(r NetCheckResult) string {
	switch {
	case r.Payload:
		return "ok"
	case r.TCP:
		return "MTU black hole: the connection completes but a 1400-byte payload does not survive. " +
			"Handshakes will succeed and large responses will hang forever."
	case r.DNS:
		return "Data plane blocked: the name resolves but the connection times out. " +
			"Open 4789/udp (VXLAN) and IP protocol 50 (ESP, for the encrypted overlay) between these nodes."
	default:
		return "DNS did not resolve: the swarm's gossip is not reaching this node. Open 7946/tcp and 7946/udp."
	}
}

func summarize(results []NetCheckResult) (bool, string) {
	var bad int
	for _, r := range results {
		if !r.Payload {
			bad++
		}
	}
	if bad == 0 {
		return true, fmt.Sprintf("All %d cross-node paths carry DNS, TCP and a %d-byte payload.", len(results), probePayload)
	}
	return false, fmt.Sprintf("%d of %d cross-node paths are broken.", bad, len(results))
}

func randSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano()%1e6)
}
