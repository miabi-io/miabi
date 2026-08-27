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

// The control plane is not on the proxy network, and it talks to the registry over the network — every
// browse, quota and GC call goes to http://mb-registry:5000 (see NewService). So the registry has to be
// on the private network, or the built-in registry's whole admin surface fails with "no such host".
func TestRegistryIsReachableFromTheControlPlane(t *testing.T) {
	s := &Service{network: "miabi", internalNetwork: "miabi-internal"}
	dc := &netDocker{}

	nets, err := s.networks(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(nets, "miabi-internal") {
		t.Errorf("registry networks = %v, missing the private network the control plane dials it on", nets)
	}
	// It keeps the proxy attachment for its own egress — an S3 backend may be a self-hosted MinIO app —
	// and Docker picks the default route from the attachments, so that one goes first.
	if len(nets) == 0 || nets[0] != "miabi" {
		t.Errorf("registry networks = %v, want the egress-carrying network first", nets)
	}
}

// Compose has no private network, and the registry there has always been on the proxy network alone.
func TestRegistryOnComposeIsUnchanged(t *testing.T) {
	s := &Service{network: "miabi"}
	nets, err := s.networks(context.Background(), &netDocker{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 || nets[0] != "miabi" {
		t.Errorf("registry networks = %v, want only the proxy network", nets)
	}
}

// Docker refuses a duplicate attachment, so a manifest that names one network twice would leave the
// registry unable to start at all.
func TestRegistryDoesNotAttachTheSameNetworkTwice(t *testing.T) {
	s := &Service{network: "miabi", internalNetwork: "miabi"}
	nets, err := s.networks(context.Background(), &netDocker{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 {
		t.Errorf("registry networks = %v, want one", nets)
	}
}
