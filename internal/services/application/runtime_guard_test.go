// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// clusterOff reports a platform with no swarm, which is what strands an app that
// was made a service while there was one.
type clusterOff struct{}

func (clusterOff) IsSwarm(uint) bool         { return false }
func (clusterOff) ClusterOfServer(uint) uint { return 0 }
func (clusterOff) Manager(context.Context, uint) (docker.Client, error) {
	return nil, docker.ErrNotFound
}

// The reported bug: an app stored as a service, cluster mode since switched off,
// and every settings save refused — including the memory limit the user was
// actually editing. The console never sends runtime_kind, so there was no way out
// of it from the UI.
func TestEditingAServiceAppWithClusterOffIsAllowed(t *testing.T) {
	s := &Service{cluster: clusterOff{}}
	app := &models.Application{RuntimeKind: models.RuntimeService, Replicas: 1}

	if err := s.validateRuntime(app, models.RuntimeService); err != nil {
		t.Fatalf("editing an app that was already a service was refused: %v", err)
	}
	if app.RuntimeKind != models.RuntimeService {
		t.Errorf("runtime changed to %q behind the user's back", app.RuntimeKind)
	}
}

// The rule that still has to hold: nobody may newly ask for a service runtime the
// platform cannot run.
func TestTurningAnAppIntoAServiceWithClusterOffIsRefused(t *testing.T) {
	s := &Service{cluster: clusterOff{}}
	app := &models.Application{RuntimeKind: models.RuntimeService, Replicas: 1}

	if err := s.validateRuntime(app, models.RuntimeContainer); !errors.Is(err, ErrClusterDisabled) {
		t.Errorf("err = %v, want ErrClusterDisabled", err)
	}
	// Create passes "" for the stored kind: nothing exists yet, so any service is new.
	if err := s.validateRuntime(app, ""); !errors.Is(err, ErrClusterDisabled) {
		t.Errorf("create err = %v, want ErrClusterDisabled", err)
	}
}

// An app the platform silently promoted to a service in cluster mode is demoted
// when that cluster goes away, rather than handed to the user as an error about a
// decision they never made.
func TestAutoPromotedServiceIsDemotedNotRefused(t *testing.T) {
	s := &Service{cluster: clusterOff{}}
	app := &models.Application{
		RuntimeKind: models.RuntimeService,
		Replicas:    1,
		Metadata:    models.Metadata{models.MetaRuntimeAutoService: "true"},
	}

	if err := s.validateRuntime(app, models.RuntimeService); err != nil {
		t.Fatalf("an auto-promoted service was refused: %v", err)
	}
	if app.RuntimeKind != models.RuntimeContainer {
		t.Errorf("runtime = %q, want container: the platform chose it, so the platform un-chooses it", app.RuntimeKind)
	}
	if app.Metadata[models.MetaRuntimeAutoService] == "true" {
		t.Error("the auto-service marker survived the demotion, so it would be re-evaluated forever")
	}
}

// With a working cluster nothing is demoted and nothing is refused.
type clusterOn struct{}

func (clusterOn) IsSwarm(uint) bool         { return true }
func (clusterOn) ClusterOfServer(uint) uint { return 0 }
func (clusterOn) Manager(context.Context, uint) (docker.Client, error) {
	return nil, docker.ErrNotFound
}

func TestServiceRuntimeIsUntouchedWithAClusterUp(t *testing.T) {
	s := &Service{cluster: clusterOn{}}
	app := &models.Application{
		RuntimeKind: models.RuntimeService,
		Replicas:    1,
		Metadata:    models.Metadata{models.MetaRuntimeAutoService: "true"},
	}

	if err := s.validateRuntime(app, models.RuntimeContainer); err != nil {
		t.Fatalf("promoting to a service on a live cluster was refused: %v", err)
	}
	if app.RuntimeKind != models.RuntimeService {
		t.Errorf("runtime = %q, want service", app.RuntimeKind)
	}
}

// Swarm services have no device mapping, so a device grant would be silently dropped.
func TestServiceRuntimeRefusesDevices(t *testing.T) {
	s := &Service{cluster: clusterOn{}}
	app := &models.Application{RuntimeKind: models.RuntimeService, Replicas: 1, Devices: []string{"/dev/net/tun"}}

	if err := s.validateRuntime(app, ""); !errors.Is(err, models.ErrDevicesOnService) {
		t.Errorf("service with devices err = %v, want ErrDevicesOnService", err)
	}
	app.RuntimeKind = models.RuntimeContainer
	if err := s.validateRuntime(app, ""); err != nil {
		t.Errorf("container with devices was refused: %v", err)
	}
}
