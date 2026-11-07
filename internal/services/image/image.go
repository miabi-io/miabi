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

// Package image manages the built-image catalog; its GC never collects a digest a live or pinned release uses.
package image

import (
	"context"
	"errors"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound  = errors.New("image not found")
	ErrInUse     = errors.New("image is referenced by a live deployment or pinned release")
	ErrNoDigest  = errors.New("image digest is required")
	ErrNoWorkspc = errors.New("workspace is required")
)

// A nil Remover skips physical removal (DB-only).
type Remover interface {
	RemoveImage(ctx context.Context, ref string, force bool) error
}

// Service is the image catalog: write provenance, list, and GC.
type Service struct {
	images   *repositories.ImageRepository
	releases *repositories.ReleaseRepository
}

// NewService wires the image catalog service.
func NewService(images *repositories.ImageRepository, releases *repositories.ReleaseRepository) *Service {
	return &Service{images: images, releases: releases}
}

// RecordInput is the provenance written after a successful build.
type RecordInput struct {
	WorkspaceID   uint
	Repository    string // repo name, e.g. "miabi/app-5"
	Tag           string // human tag, e.g. the run number
	Digest        string // sha256:… (image id for a local-only build)
	SizeBytes     int64
	ApplicationID *uint
	PipelineRunID *uint
	Commit        string
	Runner        string // "internal" | "<runner-name>"
}

// (workspace, digest) is unique, so re-recording the same digest updates provenance instead of duplicating.
func (s *Service) Record(in RecordInput) (*models.Image, error) {
	if in.WorkspaceID == 0 {
		return nil, ErrNoWorkspc
	}
	if in.Digest == "" {
		return nil, ErrNoDigest
	}
	now := time.Now()
	if existing, err := s.images.FindByDigest(in.WorkspaceID, in.Digest); err == nil {
		existing.Repository = in.Repository
		existing.Tag = in.Tag
		existing.SizeBytes = in.SizeBytes
		existing.ApplicationID = in.ApplicationID
		existing.PipelineRunID = in.PipelineRunID
		existing.Commit = in.Commit
		existing.Runner = in.Runner
		existing.BuiltAt = &now
		if err := s.images.Update(existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	img := &models.Image{
		WorkspaceID:   in.WorkspaceID,
		Repository:    in.Repository,
		Tag:           in.Tag,
		Digest:        in.Digest,
		SizeBytes:     in.SizeBytes,
		ApplicationID: in.ApplicationID,
		PipelineRunID: in.PipelineRunID,
		Commit:        in.Commit,
		Runner:        in.Runner,
		BuiltAt:       &now,
	}
	if err := s.images.Create(img); err != nil {
		return nil, err
	}
	return img, nil
}

func (s *Service) SetTag(imageID uint, tag string) error { return s.images.SetTag(imageID, tag) }

func (s *Service) List(workspaceID uint, appID *uint) ([]models.Image, error) {
	if appID != nil {
		return s.images.ListByApp(workspaceID, *appID)
	}
	return s.images.ListByWorkspace(workspaceID)
}

// Get returns one catalog image scoped to the workspace.
func (s *Service) Get(workspaceID, id uint) (*models.Image, error) {
	img, err := s.images.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return img, nil
}

// Delete refuses images protected by a live or pinned release.
func (s *Service) Delete(ctx context.Context, workspaceID, id uint, remover Remover) error {
	img, err := s.images.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	protected, err := s.protectedSet()
	if err != nil {
		return err
	}
	if protected.holds(img) {
		return ErrInUse
	}
	if remover != nil && img.Repository != "" {
		_ = remover.RemoveImage(ctx, img.Ref(), false)
	}
	return s.images.Delete(workspaceID, id)
}

// Protected images (live or pinned releases) are exempt regardless.
type RetentionPolicy struct {
	KeepPerApp int
	MinAge     time.Duration
}

func (p RetentionPolicy) withDefaults() RetentionPolicy {
	if p.KeepPerApp <= 0 {
		p.KeepPerApp = 10
	}
	if p.MinAge <= 0 {
		p.MinAge = 24 * time.Hour
	}
	return p
}

// GCReport summarizes a sweep.
type GCReport struct {
	Deleted   int   `json:"deleted"`
	Protected int   `json:"protected"`
	FreedBy   int64 `json:"freed_bytes"`
}

// GC never deletes an image a live or pinned release references; remover may be nil (DB-only).
func (s *Service) GC(ctx context.Context, policy RetentionPolicy, remover Remover) (GCReport, error) {
	policy = policy.withDefaults()
	all, err := s.images.ListAll()
	if err != nil {
		return GCReport{}, err
	}
	protected, err := s.protectedSet()
	if err != nil {
		return GCReport{}, err
	}
	cutoff := time.Now().Add(-policy.MinAge)

	// ListAll is ordered application_id ASC, created_at DESC, so per app the newest come first.
	var report GCReport
	keptPerApp := map[uint]int{}
	for i := range all {
		img := &all[i]
		appKey := uint(0)
		if img.ApplicationID != nil {
			appKey = *img.ApplicationID
		}
		if protected.holds(img) {
			report.Protected++
			keptPerApp[appKey]++ // a protected image still counts toward "kept"
			continue
		}
		if keptPerApp[appKey] < policy.KeepPerApp {
			keptPerApp[appKey]++
			continue
		}
		if created := img.CreatedAt; created.After(cutoff) {
			continue // too young to collect
		}
		if remover != nil && img.Repository != "" {
			_ = remover.RemoveImage(ctx, img.Ref(), false)
		}
		if err := s.images.Delete(img.WorkspaceID, img.ID); err != nil {
			continue
		}
		report.Deleted++
		report.FreedBy += img.SizeBytes
	}
	return report, nil
}

// protectedRefs holds the image IDs, digests, and refs that GC must never touch.
type protectedRefs struct {
	ids     map[uint]bool
	digests map[string]bool
	refs    map[string]bool
}

func (p protectedRefs) holds(img *models.Image) bool {
	if img.ID != 0 && p.ids[img.ID] {
		return true
	}
	if img.Digest != "" && p.digests[img.Digest] {
		return true
	}
	if r := img.Ref(); r != "" && p.refs[r] {
		return true
	}
	return false
}

// ProtectedDigests exposes GC's exemption set so other delete paths can refuse removing a running image.
func (s *Service) ProtectedDigests() (map[string]bool, error) {
	p, err := s.protectedSet()
	if err != nil {
		return nil, err
	}
	return p.digests, nil
}

// Hand-pushed images have no catalog row and are simply absent.
func (s *Service) ByDigests(workspaceID uint, digests []string) (map[string]models.Image, error) {
	rows, err := s.images.ListByDigests(workspaceID, digests)
	if err != nil {
		return nil, err
	}
	out := make(map[string]models.Image, len(rows))
	for _, r := range rows {
		out[r.Digest] = r
	}
	return out, nil
}

func (s *Service) protectedSet() (protectedRefs, error) {
	p := protectedRefs{ids: map[uint]bool{}, digests: map[string]bool{}, refs: map[string]bool{}}
	rels, err := s.releases.ListProtected()
	if err != nil {
		return p, err
	}
	for _, rel := range rels {
		if rel.ImageID != nil {
			p.ids[*rel.ImageID] = true
		}
		if rel.Digest != "" {
			p.digests[rel.Digest] = true
		}
		if rel.Image != "" {
			p.refs[rel.Image] = true
		}
	}
	return p, nil
}
