// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"context"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/job"
)

// SetJobs wires the job service (kinds CronJob and Job). Nil leaves a manifest
// declaring either failing with ErrUnsupportedKind.
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
				SpecFP:         d.SpecHash,
			},
		})
	}
	return nil
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

// jobMetadata stamps the ownership labels prune is scoped by; a manifest can't
// spoof provenance through its own labels.
func jobMetadata(ctx context.Context, desired declarative.Resource) models.Metadata {
	return tagSource(ctx, models.SetBuiltin(models.SanitizeUserMetadata(desired.Metadata.Labels),
		models.MetaManagedBy, ManagedByGitOps))
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
	in := job.CronJobInput{
		Name: ch.Name, Schedule: spec.Schedule, Command: spec.Command, Entrypoint: spec.Entrypoint,
		Image: spec.Image, RegistryID: regID, RunAsUser: spec.RunAsUser(), TimeoutSecs: spec.TimeoutSeconds,
		ConcurrencyPolicy: spec.ConcurrencyPolicy, HistoryLimit: spec.HistoryLimit,
		Metadata: jobMetadata(ctx, desired), Annotations: desired.Metadata.Annotations,
	}
	if ch.Action == declarative.ActionCreate {
		in.Enabled = spec.Suspend == nil || !*spec.Suspend
		_, err = s.jobs.CreateCronJob(workspaceID, app.ID, in)
		return err
	}
	cj, err := s.jobs.GetCronJobByName(workspaceID, ch.Name)
	if err != nil {
		return fmt.Errorf("cronjob %q: %w", ch.Name, err)
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
	in := job.DefinitionInput{
		Name: ch.Name, ApplicationID: app.ID, Command: spec.Command, Entrypoint: spec.Entrypoint,
		Image: spec.Image, RegistryID: regID, RunAsUser: spec.RunAsUser(), TimeoutSecs: spec.TimeoutSeconds,
		RunPolicy: spec.Policy(), WaitForDeploy: spec.Waits(), HistoryLimit: spec.HistoryLimit,
		BackoffLimit: spec.BackoffLimit, SpecHash: spec.Fingerprint(), Metadata: jobMetadata(ctx, desired), Annotations: desired.Metadata.Annotations,
	}
	_, _, err = s.jobs.SaveDefinition(ctx, workspaceID, in, run)
	return err
}

// jobRun decides whether applying a Job change starts a run: a create runs unless
// it adopts an existing job, an update runs only an onChange Job whose run
// template changed, and a once Job's template can't change at all.
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

// refuseRunMove fails moving a CronJob or Job to another app: its history and
// the schedule's runs belong to the app it was created in.
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
			if r.CronJob != nil && r.CronJob.Suspend != nil && !*r.CronJob.Suspend {
				r.CronJob.Suspend = nil
			}
			if r.Job != nil && r.Job.Waits() == (r.Job.Policy() == declarative.RunOnChange) {
				r.Job.WaitForDeploy = nil
			}
			out.Add(r)
		}
	}
}

// failedJobs names the declared Jobs owned by source whose last run failed or
// was skipped. Empty without a source: only a GitOps sync reports them.
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
