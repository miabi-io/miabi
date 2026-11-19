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
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/job"
	"github.com/miabi-io/miabi/internal/services/placement"
)

// Nil makes a manifest declaring either fail with ErrUnsupportedKind.
func (s *Service) SetJobs(j *job.Service) { s.jobs = j }

// snapshotJobs adds the workspace's CronJobs and Job definitions to the live set.
func (s *Service) snapshotJobs(workspaceID uint, set *declarative.ResourceSet, appSlugByID, regNameByID map[uint]string) error {
	if s.jobs == nil {
		return nil
	}
	crons, err := s.jobs.ListCronJobs(workspaceID, 0)
	if err != nil {
		return fmt.Errorf("snapshot cronjobs: %w", err)
	}
	for i := range crons {
		cj := crons[i]
		suspended := !cj.Enabled
		env, secretEnv := jobEnvSpec(cj.Env)
		inherit := models.InheritsEnv(cj.InheritEnv)
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindCronJob,
			Metadata: metaA(cj.UID, cj.Name, cj.Metadata, cj.Annotations),
			CronJob: &declarative.CronJobSpec{
				App:               appSlugByID[cj.ApplicationID],
				Schedule:          cj.Schedule,
				Command:           cj.Command,
				Entrypoint:        cj.Entrypoint,
				Image:             cj.Image,
				Registry:          registryName(regNameByID, cj.RegistryID),
				TimeoutSeconds:    cj.TimeoutSecs,
				ConcurrencyPolicy: cj.ConcurrencyPolicy,
				HistoryLimit:      cj.HistoryLimit,
				Suspend:           &suspended,
				Security:          securitySpec(cj.RunAsUser),
				Placement:         jobPlacement(cj.Node),
				Env:               env,
				SecretEnv:         secretEnv,
				InheritEnv:        &inherit,
			},
		})
	}
	defs, err := s.jobs.ListDefinitions(workspaceID, 0)
	if err != nil {
		return fmt.Errorf("snapshot job definitions: %w", err)
	}
	for i := range defs {
		d := defs[i]
		wait := d.WaitForDeploy
		env, secretEnv := jobEnvSpec(d.Env)
		inherit := models.InheritsEnv(d.InheritEnv)
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindJob,
			Metadata: metaA(d.UID, d.Name, d.Metadata, d.Annotations),
			Job: &declarative.JobSpec{
				App:            appSlugByID[d.ApplicationID],
				Command:        d.Command,
				Entrypoint:     d.Entrypoint,
				Image:          d.Image,
				Registry:       registryName(regNameByID, d.RegistryID),
				TimeoutSeconds: d.TimeoutSecs,
				RunPolicy:      d.RunPolicy,
				WaitForDeploy:  &wait,
				HistoryLimit:   d.HistoryLimit,
				BackoffLimit:   d.BackoffLimit,
				Security:       securitySpec(d.RunAsUser),
				Placement:      jobPlacement(d.Node),
				Env:            env,
				SecretEnv:      secretEnv,
				InheritEnv:     &inherit,
				SpecFP:         d.SpecHash,
			},
		})
	}
	return nil
}

// Secret values are blanked: the plan masks them, comparing only presence and secrecy.
func jobEnvSpec(env models.JobEnv) (map[string]string, []string) {
	if len(env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	var secretEnv []string
	for _, v := range env {
		if v.IsSecret {
			secretEnv = append(secretEnv, v.Key)
			out[v.Key] = ""
			continue
		}
		out[v.Key] = v.Value
	}
	return out, secretEnv
}

// Strict, like an app's env: a reference that still cannot resolve at execution time is a real error.
func (s *Service) renderJobEnv(workspaceID uint, kind string, env map[string]string, secretEnv []string, set *declarative.ResourceSet) (models.JobEnv, error) {
	if len(env) == 0 {
		return nil, nil
	}
	r := declarative.NewRenderer(declarative.RenderContext{
		Databases: s.databaseViews(workspaceID),
		Secrets:   s.secretViews(workspaceID),
		Apps:      s.appViews(workspaceID, set),
	})
	rendered, err := r.RenderEnv(kind, env)
	if err != nil {
		return nil, err
	}
	secret := withKeys(append([]string(nil), secretEnv...), r.CredentialKeys())
	keys := make([]string, 0, len(rendered))
	for k := range rendered {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(models.JobEnv, 0, len(keys))
	for _, k := range keys {
		out = append(out, models.JobEnvVar{Key: k, Value: rendered[k], IsSecret: slices.Contains(secret, k)})
	}
	return out, nil
}

func jobPlacement(node string) *declarative.JobPlacementSpec {
	if node == "" {
		return nil
	}
	return &declarative.JobPlacementSpec{Node: node}
}

// 0 with ok for an unstated node; the caller keeps the current pin then.
func (s *Service) jobNode(ctx context.Context, workspaceID uint, kind, name string, app *models.Application, node string) (uint, bool, error) {
	if node == "" || s.placer == nil {
		return 0, false, nil
	}
	res, err := s.placer.Place(placement.Request{
		WorkspaceID: workspaceID, Location: s.placer.LocationName(app.ClusterID), Node: node, Admin: isAdmin(ctx),
	})
	if err != nil {
		return 0, false, fmt.Errorf("%w: %s %q: %v", ErrInvalidManifest, kind, name, err)
	}
	return res.Pinned, true, nil
}

func registryName(names map[uint]string, id *uint) string {
	if id == nil {
		return ""
	}
	return names[*id]
}

func securitySpec(runAsUser string) *declarative.SecuritySpec {
	if runAsUser == "" {
		return nil
	}
	return &declarative.SecuritySpec{RunAsUser: runAsUser}
}

// A manifest can't spoof provenance through its own labels.
func jobMetadata(ctx context.Context, desired declarative.Resource) models.Metadata {
	return tagSource(ctx, models.SetBuiltin(models.SanitizeUserMetadata(desired.Metadata.Labels),
		models.MetaManagedBy, managedBy(ctx)))
}

func (s *Service) runRegistry(workspaceID uint, kind, name, registry string) (*uint, error) {
	if registry == "" {
		return nil, nil
	}
	reg, err := s.registries.FindByName(workspaceID, registry)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %q: unknown registry %q", ErrInvalidManifest, kind, name, registry)
	}
	return &reg.ID, nil
}

func (s *Service) applyCronJob(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	if s.jobs == nil {
		return fmt.Errorf("%w: %s", ErrUnsupportedKind, declarative.KindCronJob)
	}
	if ch.Action == declarative.ActionDelete {
		cj, err := s.jobs.GetCronJobByName(workspaceID, ch.Name)
		if err != nil {
			return fmt.Errorf("cronjob %q: %w", ch.Name, err)
		}
		return s.jobs.DeleteCronJob(workspaceID, cj.ID)
	}
	if err := refuseImmutable(ch); err != nil {
		return err
	}
	spec := desired.CronJob
	if spec == nil {
		return fmt.Errorf("%w: cronjob %q: spec is required", ErrInvalidManifest, ch.Name)
	}
	app, err := s.findApp(workspaceID, spec.App)
	if err != nil {
		return fmt.Errorf("cronjob %q: %w", ch.Name, err)
	}
	regID, err := s.runRegistry(workspaceID, "cronjob", ch.Name, spec.Registry)
	if err != nil {
		return err
	}
	env, err := s.renderJobEnv(workspaceID, "cronjob", spec.Env, spec.SecretEnv, nil)
	if err != nil {
		return fmt.Errorf("%w: cronjob %q: %v", ErrInvalidManifest, ch.Name, err)
	}
	inherit := spec.Inherits()
	in := job.CronJobInput{
		Name: ch.Name, Schedule: spec.Schedule, Command: spec.Command, Entrypoint: spec.Entrypoint,
		Image: spec.Image, RegistryID: regID, RunAsUser: spec.RunAsUser(), TimeoutSecs: spec.TimeoutSeconds,
		ConcurrencyPolicy: spec.ConcurrencyPolicy, HistoryLimit: spec.HistoryLimit,
		Env: env, InheritEnv: &inherit,
		Metadata: jobMetadata(ctx, desired), Annotations: desired.Metadata.Annotations,
	}
	pin, stated, err := s.jobNode(ctx, workspaceID, "cronjob", ch.Name, app, spec.NodeName())
	if err != nil {
		return err
	}
	in.PinnedServerID = pin
	if ch.Action == declarative.ActionCreate {
		in.Enabled = spec.Suspend == nil || !*spec.Suspend
		_, err = s.jobs.CreateCronJob(workspaceID, app.ID, in)
		return err
	}
	cj, err := s.jobs.GetCronJobByName(workspaceID, ch.Name)
	if err != nil {
		return fmt.Errorf("cronjob %q: %w", ch.Name, err)
	}
	if !stated {
		in.PinnedServerID = cj.PinnedServerID
	}
	in.Enabled = cj.Enabled
	if spec.Suspend != nil {
		in.Enabled = !*spec.Suspend
	}
	_, err = s.jobs.UpdateCronJob(workspaceID, cj.ID, in)
	return err
}

func (s *Service) applyJob(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	if s.jobs == nil {
		return fmt.Errorf("%w: %s", ErrUnsupportedKind, declarative.KindJob)
	}
	if ch.Action == declarative.ActionDelete {
		def, err := s.jobs.GetDefinitionByName(workspaceID, ch.Name)
		if err != nil {
			return fmt.Errorf("job %q: %w", ch.Name, err)
		}
		return s.jobs.DeleteDefinition(ctx, workspaceID, def.ID)
	}
	if err := refuseImmutable(ch); err != nil {
		return err
	}
	spec := desired.Job
	if spec == nil {
		return fmt.Errorf("%w: job %q: spec is required", ErrInvalidManifest, ch.Name)
	}
	run, err := jobRun(ch, desired)
	if err != nil {
		return err
	}
	app, err := s.findApp(workspaceID, spec.App)
	if err != nil {
		return fmt.Errorf("job %q: %w", ch.Name, err)
	}
	regID, err := s.runRegistry(workspaceID, "job", ch.Name, spec.Registry)
	if err != nil {
		return err
	}
	env, err := s.renderJobEnv(workspaceID, "job", spec.Env, spec.SecretEnv, nil)
	if err != nil {
		return fmt.Errorf("%w: job %q: %v", ErrInvalidManifest, ch.Name, err)
	}
	inherit := spec.Inherits()
	in := job.DefinitionInput{
		Name: ch.Name, ApplicationID: app.ID, Command: spec.Command, Entrypoint: spec.Entrypoint,
		Image: spec.Image, RegistryID: regID, RunAsUser: spec.RunAsUser(), TimeoutSecs: spec.TimeoutSeconds,
		Env: env, InheritEnv: &inherit,
		RunPolicy: spec.Policy(), WaitForDeploy: spec.Waits(), HistoryLimit: spec.HistoryLimit,
		BackoffLimit: spec.BackoffLimit, SpecHash: spec.Fingerprint(), Metadata: jobMetadata(ctx, desired), Annotations: desired.Metadata.Annotations,
	}
	pin, stated, err := s.jobNode(ctx, workspaceID, "job", ch.Name, app, spec.NodeName())
	if err != nil {
		return err
	}
	if stated {
		in.PinnedServerID = &pin
	}
	_, _, err = s.jobs.SaveDefinition(ctx, workspaceID, in, run)
	return err
}

// A create runs unless adopting; an update runs only an onChange Job whose template changed.
func jobRun(ch declarative.Change, desired declarative.Resource) (bool, error) {
	spec := desired.Job
	specChanged := hasField(ch, declarative.JobSpecField)
	switch ch.Action {
	case declarative.ActionCreate:
		return desired.Metadata.Annotations[declarative.AnnotationSkipInitialRun] != "true", nil
	case declarative.ActionUpdate:
		if specChanged && spec.Policy() == declarative.RunOnce {
			return false, fmt.Errorf("%w: job %q runs once, so its command, image and settings can't change; rename it to run a new one",
				ErrInvalidManifest, ch.Name)
		}
		return specChanged && spec.Policy() == declarative.RunOnChange, nil
	}
	return false, nil
}

func hasField(ch declarative.Change, field string) bool {
	for _, f := range ch.Fields {
		if f.Field == field {
			return true
		}
	}
	return false
}

// History and the schedule's runs belong to the app it was created in.
func refuseRunMove(ch declarative.Change) error {
	if ch.Kind != declarative.KindCronJob && ch.Kind != declarative.KindJob {
		return nil
	}
	for _, f := range ch.Fields {
		if f.Field == "app" {
			return fmt.Errorf("%w: %s %q runs in application %q; moving it to %q is a delete and a create — remove it, apply, then add it back",
				ErrInvalidManifest, strings.ToLower(string(ch.Kind)), ch.Name, f.From, f.To)
		}
	}
	return nil
}

// exportJobs adds an application's CronJobs and Jobs to an export bundle.
func exportJobs(set, out *declarative.ResourceSet, app string) {
	for _, kind := range []declarative.Kind{declarative.KindCronJob, declarative.KindJob} {
		for _, r := range set.ByKind(kind) {
			owner := ""
			if r.CronJob != nil {
				owner = r.CronJob.App
			} else if r.Job != nil {
				owner = r.Job.App
			}
			if owner != app {
				continue
			}
			r.Metadata = declarative.Meta{Name: r.Metadata.Name, Annotations: r.Metadata.Annotations}
			// Node names are specific to this install.
			if r.CronJob != nil {
				r.CronJob.Placement = nil
			}
			if r.Job != nil {
				r.Job.Placement = nil
			}
			if r.CronJob != nil && r.CronJob.Suspend != nil && !*r.CronJob.Suspend {
				r.CronJob.Suspend = nil
			}
			if r.CronJob != nil && r.CronJob.Inherits() {
				r.CronJob.InheritEnv = nil
			}
			if r.Job != nil && r.Job.Inherits() {
				r.Job.InheritEnv = nil
			}
			if r.Job != nil && r.Job.Waits() == (r.Job.Policy() == declarative.RunOnChange) {
				r.Job.WaitForDeploy = nil
			}
			out.Add(r)
		}
	}
}

// Empty without a source: only a GitOps sync reports them.
func (s *Service) failedJobs(workspaceID uint, source string) []string {
	if s.jobs == nil || source == "" {
		return nil
	}
	defs, err := s.jobs.ListDefinitions(workspaceID, 0)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range defs {
		if d.Metadata[models.MetaGitOpsSource] != source {
			continue
		}
		if d.LastStatus == models.JobFailed || d.LastStatus == models.JobSkipped {
			out = append(out, d.Name)
		}
	}
	return out
}
