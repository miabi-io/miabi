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

// Package versioning decides the version, and so the image tag, each pipeline run builds. A run
// reserves its version before it is dispatched, and settles it when it ends: published and tagged in
// the registry on success, released for the next run (or skipped) otherwise. Deploys stay by digest;
// the version is a readable, immutable label on the same artifact.
package versioning

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound = errors.New("version policy not found")
	// ErrPolicyExists rejects a second policy on one image repository.
	ErrPolicyExists = errors.New("this repository already has a version policy")
	// ErrManaged rejects an API edit of settings a pipeline's `versioning:` block owns.
	ErrManaged = errors.New("these settings come from the pipeline's versioning block — edit the pipeline file instead")
	// ErrTagExists fails a run whose hand-set version is already a tag in the registry.
	ErrTagExists = errors.New("the version's tag already exists in the registry")
)

// Registry tags and inspects images in the built-in registry. Satisfied by *registryserver.Service.
type Registry interface {
	TagImage(ctx context.Context, repository, digest, tag string) error
	ImageTagDigest(ctx context.Context, repository, tag string) (digest string, found bool, err error)
}

// ImageTagger relabels the image catalog entry a run produced. Satisfied by *image.Service.
type ImageTagger interface {
	SetTag(imageID uint, tag string) error
}

// maxReserveAttempts bounds re-planning after a concurrent run takes the same version.
const maxReserveAttempts = 5

// maxTagSkips bounds how far a run moves forward past versions already present in the registry.
const maxTagSkips = 20

// Service manages version policies and the versions runs reserve.
type Service struct {
	repo      *repositories.VersionRepository
	apps      *repositories.ApplicationRepository
	pipelines *repositories.PipelineRepository
	registry  Registry
	images    ImageTagger
}

func NewService(repo *repositories.VersionRepository, apps *repositories.ApplicationRepository, pipelines *repositories.PipelineRepository) *Service {
	return &Service{repo: repo, apps: apps, pipelines: pipelines}
}

// SetRegistry wires the registry used to tag published versions. Without it, versions are still
// reserved and recorded but no registry tag is written.
func (s *Service) SetRegistry(r Registry) { s.registry = r }

// SetImages wires the image catalog so a published image carries its version as its tag.
func (s *Service) SetImages(i ImageTagger) { s.images = i }

func owner(def *models.PipelineDefinition) (appID, pipelineID *uint) {
	if def.ApplicationID != nil {
		return def.ApplicationID, nil
	}
	id := def.ID
	return nil, &id
}

// ForPipeline returns the policy covering a pipeline's image repository, or ErrNotFound.
func (s *Service) ForPipeline(def *models.PipelineDefinition) (*models.VersionPolicy, error) {
	p, err := s.repo.FindPolicyFor(owner(def))
	if err != nil {
		return nil, ErrNotFound
	}
	s.decorate(p)
	return p, nil
}

// EnsureForPipeline gives a pipeline's repository a policy when it has none: settings when given,
// else the defaults. With managed, settings come from the pipeline's `versioning:` block and replace
// whatever the policy held, and the pipeline is recorded as their owner.
func (s *Service) EnsureForPipeline(def *models.PipelineDefinition, settings *Settings, managed bool) (*models.VersionPolicy, error) {
	appID, pipelineID := owner(def)
	if existing, err := s.repo.FindPolicyFor(appID, pipelineID); err == nil {
		if !managed || settings == nil {
			return existing, nil
		}
		n, err := settings.Normalize()
		if err != nil {
			return nil, err
		}
		n.apply(existing)
		existing.ManagedBy = def.Name
		return existing, s.repo.SavePolicy(existing)
	}
	st := DefaultSettings()
	if settings != nil {
		st = *settings
	}
	n, err := st.Normalize()
	if err != nil {
		return nil, err
	}
	p := &models.VersionPolicy{WorkspaceID: def.WorkspaceID, ApplicationID: appID, PipelineID: pipelineID}
	n.apply(p)
	if managed {
		p.ManagedBy = def.Name
	}
	if err := s.repo.CreatePolicy(p); err != nil {
		// A concurrent create on the same repository won the unique index; use its row.
		if existing, ferr := s.repo.FindPolicyFor(appID, pipelineID); ferr == nil {
			return existing, nil
		}
		return nil, err
	}
	return p, nil
}

// ValidateSettings normalizes settings without storing them, for checking a pipeline spec on save.
func ValidateSettings(st *Settings) error {
	if st == nil {
		return nil
	}
	_, err := st.Normalize()
	return err
}

// Unmanage hands a policy back to the API once the pipeline that owned its settings drops its block.
func (s *Service) Unmanage(def *models.PipelineDefinition) {
	p, err := s.repo.FindPolicyFor(owner(def))
	if err != nil || p.ManagedBy != def.Name {
		return
	}
	p.ManagedBy = ""
	if err := s.repo.SavePolicy(p); err != nil {
		logger.Warn("release managed version policy failed", "policy", p.ID, "error", err)
	}
}

// PipelineDeleted removes an unbound pipeline's own policy; an application's policy outlives its pipelines.
func (s *Service) PipelineDeleted(def *models.PipelineDefinition) {
	p, err := s.repo.FindPolicyFor(owner(def))
	if err != nil {
		return
	}
	if p.PipelineID != nil && *p.PipelineID == def.ID {
		if err := s.repo.DeletePolicy(p.ID); err != nil {
			logger.Warn("delete pipeline version policy failed", "policy", p.ID, "error", err)
		}
		return
	}
	s.Unmanage(def)
}

// Reserve hands a run its version. It is a no-op when the repository has no policy or the policy gives
// this run none, leaving the image on its run-<number> tag. On success run.Version and run.ImageTag are
// set; the caller persists the run.
func (s *Service) Reserve(run *models.PipelineRun, def *models.PipelineDefinition, req Request) error {
	policy, err := s.repo.FindPolicyFor(owner(def))
	if err != nil {
		if req.Bump != "" || req.Exact != "" {
			return fmt.Errorf("%w: this pipeline has no version policy", ErrOverrideNotSupported)
		}
		return nil
	}
	if req.Branch == "" {
		req.Branch = run.Branch
	}
	if req.Commit == "" {
		req.Commit = run.Commit
	}
	if req.CommitMessage == "" {
		req.CommitMessage = run.CommitMessage
	}
	a, err := s.reserve(policy.ID, run.ID, req)
	if err != nil || a == nil {
		return err
	}
	run.Version, run.ImageTag = a.Version, a.Tag
	return nil
}

func (s *Service) reserve(policyID, runID uint, req Request) (*models.VersionAllocation, error) {
	for attempt := 0; ; attempt++ {
		a, err := s.repo.Reserve(policyID, func(p *models.VersionPolicy, existing []models.VersionAllocation) (*models.VersionAllocation, error) {
			plan, err := Next(p, existing, req)
			if err != nil || plan.Skip {
				return nil, err
			}
			return &models.VersionAllocation{
				PolicyID: p.ID, Version: plan.Version, Tag: plan.Tag, Series: plan.Series, Branch: plan.Branch,
				Prerelease: plan.Prerelease, Bump: plan.Bump, Exact: plan.Exact, Note: plan.Note,
				Status: models.AllocationReserved, PipelineRunID: runID,
			}, nil
		})
		if errors.Is(err, repositories.ErrAllocationConflict) && attempt < maxReserveAttempts {
			continue
		}
		return a, err
	}
}

// VerifyTagFree checks, before a run is dispatched, that its version's tag is not already in the
// registry, as after a database restore or a push made outside Miabi. A taken version is skipped and the
// run moves to the next free one; only a hand-set version fails the run.
func (s *Service) VerifyTagFree(ctx context.Context, run *models.PipelineRun, repository string, log func(string)) error {
	if s.registry == nil || repository == "" {
		return nil
	}
	a, err := s.repo.FindAllocationByRun(run.ID)
	if err != nil || a.Status != models.AllocationReserved {
		return nil
	}
	for i := 0; i < maxTagSkips; i++ {
		digest, found, err := s.registry.ImageTagDigest(ctx, repository, a.Tag)
		if err != nil {
			// The registry being slow to answer must not hold up a build; the unique key still prevents reuse.
			logger.Warn("check version tag failed", "run", run.ID, "tag", a.Tag, "error", err)
			return nil
		}
		if !found {
			return nil
		}
		a.Status, a.Note = models.AllocationAbandoned, "tag already in the registry ("+shortDigest(digest)+")"
		if err := s.repo.SaveAllocation(a); err != nil {
			return err
		}
		if a.Exact {
			return fmt.Errorf("%w: %s:%s", ErrTagExists, repository, a.Tag)
		}
		prev := a.Tag
		next, err := s.reserve(a.PolicyID, run.ID, Request{Branch: run.Branch, Commit: run.Commit, CommitMessage: run.CommitMessage})
		if err != nil {
			return err
		}
		if next == nil {
			run.Version, run.ImageTag = "", ""
			return s.pipelines.UpdateRun(run)
		}
		log(fmt.Sprintf("version %s already exists in the registry; this run builds %s", prev, next.Tag))
		a = next
		run.Version, run.ImageTag = a.Version, a.Tag
		if err := s.pipelines.UpdateRun(run); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: %d versions in a row are already tagged", ErrTagExists, maxTagSkips)
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// Settle finishes a run's version once the run is terminal. A succeeded build is tagged with its version
// (and any floating tags) and published; anything else gives the version back. Best-effort: settling
// never changes the run's outcome.
func (s *Service) Settle(ctx context.Context, run *models.PipelineRun, digest, repository string, log func(string)) {
	a, err := s.repo.FindAllocationByRun(run.ID)
	if err != nil || a.Status != models.AllocationReserved {
		return
	}
	if run.Status != models.PipelineRunSucceeded || digest == "" {
		deleted, err := s.repo.Release(a, IsHead, "run "+string(run.Status))
		switch {
		case err != nil:
			logger.Warn("release version failed", "run", run.ID, "version", a.Version, "error", err)
		case deleted:
			log("version " + a.Tag + " released for the next run")
		default:
			log("version " + a.Tag + " skipped: a newer version was reserved while this run was building")
		}
		return
	}

	policy, err := s.repo.FindPolicy(run.WorkspaceID, a.PolicyID)
	if err != nil {
		return
	}
	a.Digest = digest
	if s.registry != nil && repository != "" {
		if err := s.registry.TagImage(ctx, repository, digest, a.Tag); err != nil {
			a.Note = "registry tag failed: " + err.Error()
			log("WARN: could not tag " + repository + ":" + a.Tag + ": " + err.Error())
		} else {
			log("tagged " + repository + ":" + a.Tag)
		}
	}
	current := ""
	if !a.Prerelease && compareVersions(a.Version, policy.Current) > 0 {
		current = a.Version
	}
	if err := s.repo.Publish(a, current); err != nil {
		logger.Warn("publish version failed", "run", run.ID, "version", a.Version, "error", err)
		return
	}
	if s.images != nil && run.ImageID != nil {
		if err := s.images.SetTag(*run.ImageID, a.Tag); err != nil {
			logger.Warn("label image with version failed", "image", *run.ImageID, "error", err)
		}
	}
	if !a.Prerelease && isReleaseBranch(policy, a.Branch) {
		s.moveFloating(ctx, policy, a, repository, log)
	}
}

// moveFloating points each floating tag at this version when it is the newest version the tag covers,
// so a 1.x hotfix never drags `latest` back from 2.0.
func (s *Service) moveFloating(ctx context.Context, p *models.VersionPolicy, a *models.VersionAllocation, repository string, log func(string)) {
	if s.registry == nil || repository == "" || len(p.FloatingTags) == 0 {
		return
	}
	existing, err := s.repo.Allocations(p.ID)
	if err != nil {
		return
	}
	for _, tmpl := range p.FloatingTags {
		tag := render(tmpl, versionVars(a.Version))
		newest := true
		for i := range existing {
			o := &existing[i]
			if o.ID == a.ID || o.Prerelease || o.Status != models.AllocationPublished {
				continue
			}
			if render(tmpl, versionVars(o.Version)) == tag && compareVersions(o.Version, a.Version) > 0 {
				newest = false
				break
			}
		}
		if !newest {
			continue
		}
		// A tag already pointing outside this policy's versions (e.g. a legacy v<release> label) is not ours to move.
		if cur, found, err := s.registry.ImageTagDigest(ctx, repository, tag); err == nil && found && cur != a.Digest && !publishedDigest(existing, cur) {
			log("WARN: " + repository + ":" + tag + " already names an image this policy did not publish; left as is")
			continue
		}
		if err := s.registry.TagImage(ctx, repository, a.Digest, tag); err != nil {
			log("WARN: could not move " + tag + ": " + err.Error())
			continue
		}
		log("moved " + repository + ":" + tag + " to " + a.Tag)
	}
}

func publishedDigest(existing []models.VersionAllocation, digest string) bool {
	for i := range existing {
		if existing[i].Status == models.AllocationPublished && existing[i].Digest == digest {
			return true
		}
	}
	return false
}

// versionVars splits any dotted version (semver or calver) into template values.
func versionVars(version string) tagVars {
	core, _, _ := strings.Cut(version, "-")
	parts := strings.Split(core, ".")
	v := tagVars{version: version}
	if len(parts) > 0 {
		v.major = parts[0]
	}
	if len(parts) > 1 {
		v.minor = parts[1]
	}
	if len(parts) > 2 {
		v.patch = parts[2]
	}
	return v
}

// compareVersions orders semver by precedence and anything else (calver) part by part, numerically.
func compareVersions(a, b string) int {
	if b == "" {
		if a == "" {
			return 0
		}
		return 1
	}
	va, errA := ParseVersion(a)
	vb, errB := ParseVersion(b)
	if errA == nil && errB == nil {
		return va.Compare(vb)
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, _ := strconv.Atoi(pa[i])
		nb, _ := strconv.Atoi(pb[i])
		if na != nb {
			return sign(na - nb)
		}
	}
	return sign(len(pa) - len(pb))
}

// List returns a workspace's policies.
func (s *Service) List(workspaceID uint) ([]models.VersionPolicy, error) {
	out, err := s.repo.ListPolicies(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		s.decorate(&out[i])
	}
	return out, nil
}

// Get resolves a policy by id or uid within a workspace.
func (s *Service) Get(workspaceID uint, ref string) (*models.VersionPolicy, error) {
	p, err := s.repo.FindPolicyByRef(workspaceID, ref)
	if err != nil {
		return nil, ErrNotFound
	}
	s.decorate(p)
	return p, nil
}

// Create adds a policy to a pipeline's image repository.
func (s *Service) Create(def *models.PipelineDefinition, settings Settings) (*models.VersionPolicy, error) {
	if _, err := s.repo.FindPolicyFor(owner(def)); err == nil {
		return nil, ErrPolicyExists
	}
	p, err := s.EnsureForPipeline(def, &settings, false)
	if err != nil {
		return nil, err
	}
	s.decorate(p)
	return p, nil
}

// Update replaces a policy's settings.
func (s *Service) Update(workspaceID uint, ref string, settings Settings) (*models.VersionPolicy, error) {
	p, err := s.Get(workspaceID, ref)
	if err != nil {
		return nil, err
	}
	if p.ManagedBy != "" {
		return nil, ErrManaged
	}
	n, err := settings.Normalize()
	if err != nil {
		return nil, err
	}
	n.apply(p)
	if err := s.repo.SavePolicy(p); err != nil {
		return nil, err
	}
	s.decorate(p)
	return p, nil
}

// SwitchSeries moves a semver policy to another version line. Moving back to an older line continues
// from the highest version it ever handed out, so a maintenance line never repeats a version.
func (s *Service) SwitchSeries(workspaceID uint, ref, series string) (*models.VersionPolicy, error) {
	p, err := s.Get(workspaceID, ref)
	if err != nil {
		return nil, err
	}
	if p.ManagedBy != "" {
		return nil, ErrManaged
	}
	if p.Scheme != models.VersionSchemeSemver {
		return nil, fmt.Errorf("%w: only semver policies have a series", ErrInvalidSettings)
	}
	parsed, err := ParseSeries(series)
	if err != nil {
		return nil, err
	}
	p.Series = parsed.String()
	if start, err := ParseVersion(p.Start); err != nil || !parsed.Contains(start) {
		p.Start = parsed.First(DefaultStart).String()
	}
	p.Bump = parsed.Clamp(p.Bump)
	if err := s.repo.SavePolicy(p); err != nil {
		return nil, err
	}
	s.decorate(p)
	return p, nil
}

// Delete removes a policy; the repository's builds go back to run-<number> tags.
func (s *Service) Delete(workspaceID uint, ref string) error {
	p, err := s.Get(workspaceID, ref)
	if err != nil {
		return err
	}
	if p.ManagedBy != "" {
		return ErrManaged
	}
	return s.repo.DeletePolicy(p.ID)
}

// Allocations returns a page of a policy's version history, newest first.
func (s *Service) Allocations(workspaceID uint, ref string, limit, offset int) ([]models.VersionAllocation, int64, error) {
	p, err := s.Get(workspaceID, ref)
	if err != nil {
		return nil, 0, err
	}
	out, total, err := s.repo.ListAllocations(p.ID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]uint, len(out))
	for i := range out {
		ids[i] = out[i].PipelineRunID
	}
	if refs, err := s.repo.RunRefs(ids); err == nil {
		for i := range out {
			ref := refs[out[i].PipelineRunID]
			out[i].RunNumber, out[i].PipelineID = ref.Number, ref.PipelineID
		}
	}
	return out, total, nil
}

// Preview lists the next count versions a branch would receive.
func (s *Service) Preview(workspaceID uint, ref, branch string, count int) ([]Plan, error) {
	p, err := s.Get(workspaceID, ref)
	if err != nil {
		return nil, err
	}
	existing, err := s.repo.Allocations(p.ID)
	if err != nil {
		return nil, err
	}
	return Preview(p, existing, branch, count, time.Now())
}

// PreviewSettings lists the versions unsaved settings would produce on top of a policy's history, so a
// form can show the effect of an edit before it is saved. A nil policy previews a new one.
func (s *Service) PreviewSettings(p *models.VersionPolicy, settings Settings, branch string, count int) ([]Plan, error) {
	n, err := settings.Normalize()
	if err != nil {
		return nil, err
	}
	sim := &models.VersionPolicy{}
	n.apply(sim)
	var existing []models.VersionAllocation
	if p != nil {
		if existing, err = s.repo.Allocations(p.ID); err != nil {
			return nil, err
		}
	}
	return Preview(sim, existing, branch, count, time.Now())
}

func (s *Service) decorate(p *models.VersionPolicy) {
	switch {
	case p.ApplicationID != nil && s.apps != nil:
		p.OwnerKind = "application"
		if app, err := s.apps.FindByID(*p.ApplicationID); err == nil {
			p.Repository = app.Name
		}
	case p.PipelineID != nil && s.pipelines != nil:
		p.OwnerKind = "pipeline"
		if def, err := s.pipelines.FindByID(*p.PipelineID); err == nil {
			p.Repository = def.Name
		}
	}
	if existing, err := s.repo.Allocations(p.ID); err == nil {
		if plans, err := Preview(p, existing, "", 1, time.Now()); err == nil && len(plans) == 1 {
			p.Next = plans[0].Tag
		}
	}
}
