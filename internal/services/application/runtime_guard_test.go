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

package application

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type clusterOff struct{}

func (clusterOff) IsSwarm(uint) bool         { return false }
func (clusterOff) ClusterOfServer(uint) uint { return 0 }
func (clusterOff) Manager(context.Context, uint) (docker.Client, error) {
	return nil, docker.ErrNotFound
}

// Regression: with cluster mode off, every settings save on a service app was refused.
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

// A platform-promoted service is demoted, not reported as an error about a choice the user never made.
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
