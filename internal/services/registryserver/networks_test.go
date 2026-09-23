// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

type netDocker struct {
	docker.Client
	ensured []string
	nets    []docker.Network
	listErr error
}

func (n *netDocker) EnsureNetwork(_ context.Context, name string) (string, error) {
	n.ensured = append(n.ensured, name)
	return name, nil
}

func (n *netDocker) ListNetworks(context.Context) ([]docker.Network, error) {
	return n.nets, n.listErr
}

// internalNet is a private network as the platform stack labels it.
func internalNet(name string) docker.Network {
	return docker.Network{
		ID: "abc123def456", Name: name,
		Labels: map[string]string{docker.LabelRole: docker.RolePlatformInternal},
	}
}

// Tenant authorization happens in the gateway's forwardAuth middleware, so the registry belongs on the
// platform's private network and nowhere else. It also requires Basic auth there (see upstreamauth.go);
// the network is the first boundary, not the only one.
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

// MIABI_INTERNAL_NETWORK defaults to empty and most installs never edit it, so the name is
// discovered from the engine by its role label instead. Configuring nothing must still be safe.
func TestRegistryDiscoversThePrivateNetworkByLabel(t *testing.T) {
	dc := &netDocker{nets: []docker.Network{
		{ID: "1", Name: "miabi"},
		{ID: "2", Name: "bridge"},
		internalNet("acme_platform_internal"),
	}}
	s := &Service{network: "miabi"} // no internalNetwork configured

	nets, err := s.networks(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 || nets[0] != "acme_platform_internal" {
		t.Errorf("registry networks = %v, want the labelled private network", nets)
	}
}

// The name is the operator's to choose, so the label is the test — a renamed network is still found,
// and a network merely NAMED like the platform's is not mistaken for one.
func TestRegistryDiscoveryIgnoresNamesWithoutTheLabel(t *testing.T) {
	dc := &netDocker{nets: []docker.Network{{ID: "9", Name: "miabi-internal"}}}
	if _, err := (&Service{network: "miabi"}).networks(context.Background(), dc); !errors.Is(err, ErrNoInternalNetwork) {
		t.Errorf("err = %v, want ErrNoInternalNetwork — an unlabelled network is not the platform's", err)
	}
}

// The fix itself: with no private network the registry REFUSES TO START rather than falling back to the
// shared proxy network, which every app container can reach. A registry that will not start is a visible
// failure; the fallback was a silent one.
func TestRegistryRefusesTheSharedNetworkFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		dc   *netDocker
	}{
		{"engine has no private network", &netDocker{nets: []docker.Network{{ID: "1", Name: "miabi"}}}},
		{"engine cannot be listed", &netDocker{listErr: errors.New("boom")}},
		{"engine has no networks", &netDocker{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{network: "miabi"} // a proxy network IS available — and must not be used
			nets, err := s.networks(context.Background(), tc.dc)
			if !errors.Is(err, ErrNoInternalNetwork) {
				t.Fatalf("err = %v, want ErrNoInternalNetwork", err)
			}
			if slices.Contains(nets, "miabi") {
				t.Errorf("registry networks = %v, want no fallback to the shared network", nets)
			}
			if slices.Contains(tc.dc.ensured, "miabi") {
				t.Errorf("ensured = %v, want the shared network never created for the registry", tc.dc.ensured)
			}
		})
	}
}

// An explicit MIABI_INTERNAL_NETWORK still wins, so an operator can name a network discovery would
// not find (unlabelled, or one of several).
func TestRegistryPrefersTheConfiguredNetwork(t *testing.T) {
	dc := &netDocker{nets: []docker.Network{internalNet("discovered")}}
	s := &Service{network: "miabi", internalNetwork: "configured"}

	nets, err := s.networks(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 || nets[0] != "configured" {
		t.Errorf("registry networks = %v, want the configured name to win", nets)
	}
}
