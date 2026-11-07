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

package apply

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
)

// maxGitSourceDepth caps project nesting: a root, its children and their children.
const maxGitSourceDepth = 3

// GitSourceInput is a declared GitSource as the gitops service stores it.
type GitSourceInput struct {
	Name, Repo, Repository, Ref, Path, SyncPolicy, DeletePolicy string
	Prune, SelfHeal, AllowEmpty, RequireSealedSecrets           bool
	Metadata, Annotations                                       models.Metadata
}

// An interface because the gitops service itself drives this package.
type GitSources interface {
	List(workspaceID uint) ([]models.GitSource, error)
	RepositoryNames(workspaceID uint) (map[uint]string, error)
	// Declare creates the source when id is 0, else updates it.
	Declare(workspaceID, id uint, in GitSourceInput) (*models.GitSource, error)
	// Remove deletes a source, tearing its resources down only when its delete policy is cascade.
	Remove(ctx context.Context, workspaceID, id uint) error
}

// SetGitSources wires kind: GitSource.
func (s *Service) SetGitSources(g GitSources) { s.gitSources = g }

func (s *Service) snapshotGitSources(workspaceID uint, set *declarative.ResourceSet) error {
	if s.gitSources == nil {
		return nil
	}
	sources, err := s.gitSources.List(workspaceID)
	if err != nil {
		return fmt.Errorf("snapshot git sources: %w", err)
	}
	repoNames, err := s.gitSources.RepositoryNames(workspaceID)
	if err != nil {
		return fmt.Errorf("snapshot git repositories: %w", err)
	}
	for i := range sources {
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindGitSource,
			Metadata:  meta(sources[i].UID, sources[i].Name, sources[i].Metadata),
			GitSource: gitSourceSpecOf(&sources[i], repoNames),
		})
	}
	return nil
}

func gitSourceSpecOf(src *models.GitSource, repoNames map[uint]string) *declarative.GitSourceSpec {
	spec := &declarative.GitSourceSpec{
		Repo: src.RepoURL, Ref: src.Ref, Path: src.Path, SyncPolicy: string(src.SyncPolicy),
		Prune: src.Prune, SelfHeal: src.SelfHeal, AllowEmpty: src.AllowEmpty,
		RequireSealedSecrets: src.RequireSealedSecrets, DeletePolicy: src.DeletePolicy,
	}
	// A credential supplies the URL, so the manifest names the credential and not the URL it holds.
	if src.GitRepositoryID != nil {
		spec.Repo, spec.Repository = "", repoNames[*src.GitRepositoryID]
	}
	if spec.DeletePolicy == "" {
		spec.DeletePolicy = declarative.DeletePolicyOrphan
	}
	return spec
}

func (s *Service) applyGitSource(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	if s.gitSources == nil {
		return fmt.Errorf("%w: %s", ErrUnsupportedKind, declarative.KindGitSource)
	}
	sources, err := s.gitSources.List(workspaceID)
	if err != nil {
		return err
	}
	var live *models.GitSource
	for i := range sources {
		if sources[i].Name == ch.Name {
			live = &sources[i]
		}
	}
	if ch.Action == declarative.ActionDelete {
		if live == nil {
			return nil
		}
		return s.gitSources.Remove(ctx, workspaceID, live.ID)
	}
	spec := desired.GitSource
	if spec == nil {
		return fmt.Errorf("%w: gitsource %q: spec is required", ErrInvalidManifest, ch.Name)
	}
	// An update with no source of that name is a rename matched by uid.
	if ch.Action == declarative.ActionUpdate && live == nil {
		return fmt.Errorf("%w: gitsource %q: a project's name can't change after creation", ErrInvalidManifest, ch.Name)
	}
	if err := checkNesting(sources, ownerSource(ctx), ch.Name, live); err != nil {
		return err
	}
	in := GitSourceInput{
		Name: ch.Name, Repo: spec.Repo, Repository: spec.Repository, Ref: spec.Ref, Path: spec.Path,
		SyncPolicy: spec.SyncPolicy, DeletePolicy: spec.DeletePolicy,
		Prune: spec.Prune, SelfHeal: spec.SelfHeal, AllowEmpty: spec.AllowEmpty,
		RequireSealedSecrets: spec.RequireSealedSecrets,
		Annotations:          desired.Metadata.Annotations,
	}
	var id uint
	if live == nil {
		in.Metadata = tagSource(ctx, models.SetBuiltin(models.SanitizeUserMetadata(desired.Metadata.Labels),
			models.MetaManagedBy, managedBy(ctx)))
	} else {
		id = live.ID
		in.Metadata = claim(ctx, models.MergeUserMetadata(live.Metadata, desired.Metadata.Labels))
	}
	_, err = s.gitSources.Declare(workspaceID, id, in)
	return err
}

// A project managing its own ancestor would loop.
func checkNesting(sources []models.GitSource, owner, name string, live *models.GitSource) error {
	if owner == "" {
		return nil
	}
	chain := ancestry(sources, owner)
	if live != nil && slices.Contains(chain, strconv.FormatUint(uint64(live.ID), 10)) {
		return fmt.Errorf("%w: gitsource %q is this project or one of its parents, so declaring it here would loop",
			ErrInvalidManifest, name)
	}
	if len(chain) >= maxGitSourceDepth {
		return fmt.Errorf("%w: gitsource %q would nest projects %d deep; the limit is %d",
			ErrInvalidManifest, name, len(chain)+1, maxGitSourceDepth)
	}
	return nil
}

// ancestry returns the ids of the project owning a run and of every project above it, nearest first.
func ancestry(sources []models.GitSource, owner string) []string {
	parentOf := make(map[string]string, len(sources))
	for i := range sources {
		parentOf[strconv.FormatUint(uint64(sources[i].ID), 10)] = sources[i].Metadata[models.MetaGitOpsSource]
	}
	var chain []string
	for id := owner; id != "" && !slices.Contains(chain, id); id = parentOf[id] {
		chain = append(chain, id)
	}
	return chain
}
