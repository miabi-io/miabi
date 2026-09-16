// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type adoptCall struct {
	id        uint
	container string
	image     string
	config    string
}

type fakeAdopter struct{ calls []adoptCall }

func (f *fakeAdopter) AdoptGateway(id uint, container, image, configYAML string) (*models.Server, error) {
	f.calls = append(f.calls, adoptCall{id: id, container: container, image: image, config: configYAML})
	return &models.Server{ID: id, GatewayContainer: container, GatewayImported: true}, nil
}

// centralContainer is the gateway the Miabi stack installs on the manager.
func centralContainer() docker.Container {
	return docker.Container{
		ID: "g1", Names: []string{"/" + CentralContainerName}, Image: "jkaninda/goma-gateway:1.2.3",
		State: "running", Labels: map[string]string{docker.LabelRole: docker.RoleGateway},
	}
}

func withContainers(cs ...docker.Container) *fakeDC {
	dc := newFakeDC()
	for _, c := range cs {
		dc.containers[ContainerNameOf(c)] = c
	}
	return dc
}

// A fresh install ships the gateway on the manager; nobody should have to import it by hand.
func TestAdoptCentralAdoptsTheStackGateway(t *testing.T) {
	dc := withContainers(centralContainer())
	adopter := &fakeAdopter{}

	AdoptCentral(context.Background(), dc, adopter, &models.Server{ID: 1, IsLocal: true})

	if len(adopter.calls) != 1 {
		t.Fatalf("calls = %+v; want the platform gateway adopted", adopter.calls)
	}
	got := adopter.calls[0]
	if got.id != 1 || got.container != CentralContainerName || got.image != "jkaninda/goma-gateway:1.2.3" {
		t.Fatalf("adopted %+v; want the manager's node pointed at the stack gateway", got)
	}
	// The stack owns that container's spec, so its config is not copied in as something Miabi can edit.
	if got.config != "" {
		t.Fatalf("config = %q; want none copied", got.config)
	}
}

func TestAdoptCentralLeavesAnythingAlreadySettledAlone(t *testing.T) {
	cases := map[string]struct {
		dc    *fakeDC
		local *models.Server
	}{
		"already tracked": {
			dc:    withContainers(centralContainer()),
			local: &models.Server{ID: 1, IsLocal: true, GatewayContainer: CentralContainerName, GatewayImported: true},
		},
		"miabi deployed its own gateway here": {
			dc: withContainers(centralContainer(), docker.Container{
				ID: "mb", Names: []string{"/" + ContainerName}, Image: "goma", State: "running",
			}),
			local: &models.Server{ID: 1, IsLocal: true},
		},
		"no platform gateway on this host": {
			dc:    withContainers(docker.Container{ID: "x", Names: []string{"/some-app"}, Image: "nginx"}),
			local: &models.Server{ID: 1, IsLocal: true},
		},
		"no local node": {
			dc:    withContainers(centralContainer()),
			local: nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			adopter := &fakeAdopter{}
			AdoptCentral(context.Background(), tc.dc, adopter, tc.local)
			if len(adopter.calls) != 0 {
				t.Fatalf("calls = %+v; want nothing adopted", adopter.calls)
			}
		})
	}
}

// A tracked gateway that no longer exists is drift for the control manager to report — not a reason to leave
// the live platform gateway unknown.
func TestAdoptCentralIgnoresAStaleTrackedContainer(t *testing.T) {
	dc := withContainers(centralContainer())
	adopter := &fakeAdopter{}

	AdoptCentral(context.Background(), dc, adopter, &models.Server{
		ID: 1, IsLocal: true, GatewayContainer: "goma-from-a-previous-life", GatewayImported: true,
	})

	if len(adopter.calls) != 1 || adopter.calls[0].container != CentralContainerName {
		t.Fatalf("calls = %+v; want the live platform gateway adopted in place of the stale record", adopter.calls)
	}
}
