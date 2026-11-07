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

// Tenant auth happens in the gateway, so the registry belongs only on the private network (and still requires Basic auth).
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
	// The control plane and gateway both reach the registry over the private network.
	if len(nets) != 1 || nets[0] != "miabi-internal" {
		t.Errorf("registry networks = %v, want only the private network", nets)
	}
	if !slices.Contains(dc.ensured, "miabi-internal") {
		t.Errorf("ensured networks = %v, want the private network created if absent", dc.ensured)
	}
}

// MIABI_INTERNAL_NETWORK is usually empty, so the network is discovered by its role label.
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

// The label is the test: a renamed network is found, and a similarly named one without the label is not.
func TestRegistryDiscoveryIgnoresNamesWithoutTheLabel(t *testing.T) {
	dc := &netDocker{nets: []docker.Network{{ID: "9", Name: "miabi-internal"}}}
	if _, err := (&Service{network: "miabi"}).networks(context.Background(), dc); !errors.Is(err, ErrNoInternalNetwork) {
		t.Errorf("err = %v, want ErrNoInternalNetwork — an unlabelled network is not the platform's", err)
	}
}

// With no private network the registry refuses to start instead of silently falling back to the shared proxy network.
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

// An explicit MIABI_INTERNAL_NETWORK wins, for networks discovery would not find.
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
