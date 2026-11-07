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

package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// Mirrors registryserver's ws_<id> rule; what matters is that the deploy path consults it and refuses on no.
type nsDistributor struct{ host string }

func (d nsDistributor) DistributionEnabled() bool             { return true }
func (d nsDistributor) DistributionUnavailableReason() string { return "" }
func (d nsDistributor) BuildRef(workspaceID uint, appName string, deploymentID uint) string {
	return fmt.Sprintf("%s/ws_%d/%s:%d", d.host, workspaceID, appName, deploymentID)
}
func (d nsDistributor) TagReleaseVersion(context.Context, uint, string, string, int) error {
	return nil
}
func (d nsDistributor) IsBuildRef(ref string) bool     { return strings.HasPrefix(ref, d.host+"/") }
func (d nsDistributor) PushAuth() *docker.RegistryAuth { return &docker.RegistryAuth{Server: d.host} }

func (d nsDistributor) ResolveImageRef(workspaceID uint, ref string) (string, error) {
	if !d.IsBuildRef(ref) {
		return ref, nil
	}
	want := fmt.Sprintf("%s/ws_%d/", d.host, workspaceID)
	if !strings.HasPrefix(ref, want) {
		return "", errors.New("image belongs to another workspace's registry namespace")
	}
	return ref, nil
}

func TestAuthorizedImageRefRefusesForeignNamespace(t *testing.T) {
	const host = "registry.example.com"
	h := &DeployHandler{distributor: nsDistributor{host: host}}
	app := &models.Application{WorkspaceID: 7}

	// The pull would use the platform credential every namespace accepts, so it must be refused here.
	for _, ref := range []string{
		host + "/ws_8/api:latest",
		host + "/ws_8/api@sha256:d0dd0f1b3c9a5e6a0d8e9f2a1b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e",
	} {
		got, err := h.authorizedImageRef(app, ref)
		if !errors.Is(err, ErrForeignImage) {
			t.Errorf("authorizedImageRef(%q) = (%q,%v), want ErrForeignImage", ref, got, err)
		}
	}
}

func TestAuthorizedImageRefAllowsOwnAndExternal(t *testing.T) {
	const host = "registry.example.com"
	h := &DeployHandler{distributor: nsDistributor{host: host}}
	app := &models.Application{WorkspaceID: 7}

	// External images are governed by the app's own registry credential, not the platform one.
	for _, ref := range []string{
		host + "/ws_7/api:latest",
		"nginx:1.27",
		"ghcr.io/other/api:1",
		"docker.io/library/redis:7",
		"", // no image yet: the caller falls back to the app's own ref
	} {
		got, err := h.authorizedImageRef(app, ref)
		if err != nil {
			t.Errorf("authorizedImageRef(%q) errored: %v", ref, err)
			continue
		}
		if got != ref {
			t.Errorf("authorizedImageRef(%q) = %q, want it unchanged", ref, got)
		}
	}
}

// With no distributor wired there is no internal registry to cross into.
func TestAuthorizedImageRefWithoutDistributor(t *testing.T) {
	h := &DeployHandler{}
	const ref = "registry.example.com/ws_8/api:1"
	got, err := h.authorizedImageRef(&models.Application{WorkspaceID: 7}, ref)
	if err != nil || got != ref {
		t.Fatalf("= (%q,%v), want the input unchanged", got, err)
	}
}
