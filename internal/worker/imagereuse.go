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
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// It names the pull policy because that is the knob the operator has to change.
var ErrCachedImageUnauthorized = errors.New(
	"this workspace holds no credential for that image's registry, so it may not run a copy cached on " +
		"the node by another workload — attach the registry credential to the app, or use a pull policy " +
		"that fetches the image")

// Nodes are shared, so a cached image may be another tenant's; reuse only internal refs or same-host credentials.
func mayReuseCachedImage(ref string, internal bool, auth *docker.RegistryAuth) bool {
	if internal {
		return true
	}
	if auth == nil {
		return false
	}
	return registryHostOf(ref) == registryHostOf(authServer(auth.Server))
}

// dockerHub is the canonical host every Docker Hub spelling collapses to.
const dockerHub = "docker.io"

// A permissive host parse would let a credential for one registry wave through another's cached image.
func registryHostOf(ref string) string {
	ref = strings.TrimSpace(ref)
	head, _, found := strings.Cut(ref, "/")
	if !found || !looksLikeHost(head) {
		return dockerHub
	}
	host := strings.ToLower(head)
	switch host {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com", dockerHub:
		return dockerHub
	}
	return host
}

func looksLikeHost(seg string) bool {
	return strings.ContainsAny(seg, ".:") || seg == "localhost"
}

// Hand-written Server values (bare host, scheme, Docker Hub v1 URL) must resolve to the same host as the ref.
func authServer(server string) string {
	s := strings.ToLower(strings.TrimSpace(server))
	if _, rest, found := strings.Cut(s, "://"); found {
		s = rest
	}
	s, _, _ = strings.Cut(s, "/")
	if s == "" {
		return dockerHub
	}
	// A credential's Server is always a host, so a bare single label maps to Docker Hub.
	if !looksLikeHost(s) {
		return dockerHub
	}
	return s + "/"
}
