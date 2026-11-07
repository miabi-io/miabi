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
	"fmt"
	"slices"

	"github.com/miabi-io/miabi/internal/docker"
)

// Advisory: the platform token is what authorizes.
const platformUser = "_miabi"

// DistributionEnabled false means node-local images are used, so single-node installs are unaffected.
func (s *Service) DistributionEnabled() bool {
	return s.DistributionUnavailableReason() == ""
}

// Names the specific missing piece so the operator isn't re-told about a flag already set.
func (s *Service) DistributionUnavailableReason() string {
	st, err := s.Get()
	if err != nil {
		return "registry settings could not be read: " + err.Error()
	}
	if !st.Enabled {
		return "the internal registry is disabled (set MIABI_REGISTRY_ENABLED=true)"
	}
	// Disallowed storage means the registry never started, so a push would show only a connection error.
	if reason := s.StorageUnavailableReason(st); reason != "" {
		return reason
	}
	// The platform token derives from the master key, so only enablement and a resolvable host remain.
	if s.HostFor(st) == "" {
		return "the registry host cannot be resolved (set MIABI_REGISTRY_HOST, or set the default cluster's external domain so it defaults to registry.<domain>)"
	}
	return ""
}

// TagReleaseVersion mirrors the UI release number as a tag; best-effort, using the ws_<id> storage path.
func (s *Service) TagReleaseVersion(ctx context.Context, workspaceID uint, appName, digest string, version int) error {
	if !s.DistributionEnabled() {
		return nil
	}
	repo := Namespace(workspaceID) + "/" + appName
	return s.reg.TagManifest(ctx, repo, digest, fmt.Sprintf("v%d", version))
}

// ErrNotInternalRepository rejects tagging a repository outside the built-in registry.
var ErrNotInternalRepository = errors.New("not a repository of the built-in registry")

// storagePath maps a pushed repository (<host>/ws_<id>/<name>) to its path inside the registry.
func (s *Service) storagePath(repository string) (string, error) {
	ns, rest, internal := splitInternalRef(s.RegistryHost(), repository)
	if !internal || ns == "" {
		return "", fmt.Errorf("%w: %q", ErrNotInternalRepository, repository)
	}
	return ns + "/" + rest, nil
}

// TagImage points tag at an already-pushed digest in a built-in repository, without moving any blobs.
func (s *Service) TagImage(ctx context.Context, repository, digest, tag string) error {
	if !s.DistributionEnabled() {
		return nil
	}
	repo, err := s.storagePath(repository)
	if err != nil {
		return err
	}
	return s.reg.TagManifest(ctx, repo, digest, tag)
}

// found is false when the tag does not exist or distribution is off.
func (s *Service) ImageTagDigest(ctx context.Context, repository, tag string) (digest string, found bool, err error) {
	if !s.DistributionEnabled() {
		return "", false, nil
	}
	repo, err := s.storagePath(repository)
	if err != nil {
		return "", false, err
	}
	tags, err := s.reg.Tags(ctx, repo)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if !slices.Contains(tags, tag) {
		return "", false, nil
	}
	d, err := s.reg.ManifestDigest(ctx, repo, tag)
	if err != nil {
		return "", false, err
	}
	return d, true, nil
}

// Derived here so the runner's login host and push host stay identical.
func (s *Service) RegistryHost() string {
	st, err := s.Get()
	if err != nil {
		return ""
	}
	return s.HostFor(st)
}

// BuildRef uses the immutable ws_<id> namespace because the ref is pulled again later, after possible renames.
func (s *Service) BuildRef(workspaceID uint, appName string, deploymentID uint) string {
	st, err := s.Get()
	if err != nil {
		return ""
	}
	host := s.HostFor(st)
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s:%d", host, Namespace(workspaceID), appName, deploymentID)
}

func (s *Service) IsBuildRef(ref string) bool {
	return s.IsInternalRef(ref)
}

// PushAuth returns nil when distribution is unconfigured.
func (s *Service) PushAuth() *docker.RegistryAuth {
	st, err := s.Get()
	if err != nil {
		return nil
	}
	host := s.HostFor(st)
	if host == "" {
		return nil
	}
	return &docker.RegistryAuth{Server: host, Username: platformUser, Password: s.platformToken()}
}
