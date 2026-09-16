// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

type netDocker struct {
	docker.Client
	ensured []string
}

func (n *netDocker) EnsureNetwork(_ context.Context, name string) (string, error) {
	n.ensured = append(n.ensured, name)
	return name, nil
}

// The registry serves auth-less: every token and namespace check happens in the gateway's forwardAuth
// middleware. On the shared proxy network, any app container could therefore pull any workspace's images
// straight from http://mb-registry:5000 — `docker pull mb-registry:5000/ws_<id>/<app>` with no credential
// — so the registry must not be attached to it.
func TestRegistryIsNotOnTheSharedNetwork(t *testing.T) {
	s := &Service{network: "miabi", internalNetwork: "miabi-internal"}
	dc := &netDocker{}

	nets, err := s.networks(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(nets, "miabi") {
		t.Errorf("registry networks = %v, want no attachment to the shared network app containers join", nets)
	}
	// The control plane is on the private network and talks to the registry over it — every browse, quota
	// and GC call goes to http://mb-registry:5000 — and the gateway reaches it there as a route backend.
	if len(nets) != 1 || nets[0] != "miabi-internal" {
		t.Errorf("registry networks = %v, want only the private network", nets)
	}
	if !slices.Contains(dc.ensured, "miabi-internal") {
		t.Errorf("ensured networks = %v, want the private network created if absent", dc.ensured)
	}
}

// A stack that predates the network split has only the proxy network, and a registry with no network at
// all is worse than an over-reachable one: nothing could pull, including the platform's own deploys.
func TestRegistryFallsBackToTheProxyNetwork(t *testing.T) {
	s := &Service{network: "miabi"}
	nets, err := s.networks(context.Background(), &netDocker{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 || nets[0] != "miabi" {
		t.Errorf("registry networks = %v, want only the proxy network", nets)
	}
}

func TestRegistryWithNoNetworkConfiguredFails(t *testing.T) {
	s := &Service{}
	if _, err := s.networks(context.Background(), &netDocker{}); err == nil {
		t.Error("networks() = nil error, want a failure naming MIABI_INTERNAL_NETWORK")
	}
}
