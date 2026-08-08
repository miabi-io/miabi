// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package wsbackup

import (
	"context"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// applyApps recreates the workloads. Creation goes through the application service rather than the repository,
// so an app lands with the alias, placement checks, quota accounting and naming this platform gives its own.
// What Create does not accept — healthcheck, restart policy, canary, labels — is written straight after.
func (r *restoreRun) applyApps(ctx context.Context) {
	existing := map[string]*models.Application{}
	if apps, err := r.svc.App.List(r.target); err == nil {
		for i := range apps {
			existing[apps[i].Name] = &apps[i]
		}
	}
	stackIDs := map[string]uint{}
	if stacks, err := r.svc.Stack.List(r.target); err == nil {
		for i := range stacks {
			stackIDs[stacks[i].Name] = stacks[i].ID
		}
	}
	registryIDs := map[string]uint{}
	if regs, err := r.svc.Registry.List(r.target); err == nil {
		for i := range regs {
			registryIDs[regs[i].Name] = regs[i].ID
		}
	}
	repoIDs := map[string]uint{}
	if repos, err := r.svc.GitRepo.List(r.target); err == nil {
		for i := range repos {
			repoIDs[repos[i].Name] = repos[i].ID
		}
	}
	networkIDs := map[string]uint{}
	if nets, err := r.svc.Network.List(r.target); err == nil {
		for i := range nets {
			networkIDs[nets[i].Name] = nets[i].ID
		}
	}

	for i := range r.state.Apps {
		a := r.state.Apps[i]
		if cur, ok := existing[a.Name]; ok {
			r.appIDs[a.Name] = cur.ID
			r.add("app", a.Name, "skipped", "already exists; its configuration was left alone")
			continue
		}
		in := application.CreateInput{
			DisplayName:     a.DisplayName,
			Handle:          a.Name,
			SourceType:      models.AppSourceType(a.SourceType),
			Icon:            a.Icon,
			Image:           a.Image,
			Tag:             a.Tag,
			GitRepo:         a.GitRepo,
			GitRef:          a.GitRef,
			BuildMethod:     models.AppBuildMethod(a.BuildMethod),
			Builder:         a.Builder,
			Buildpacks:      a.Buildpacks,
			BuildEnv:        a.BuildEnv,
			Command:         a.Command,
			Port:            a.Port,
			MemoryBytes:     a.MemoryBytes,
			NanoCPUs:        a.NanoCPUs,
			GPUCount:        a.GPUCount,
			GPUKind:         a.GPUKind,
			RestartPolicy:   models.RestartPolicy(a.RestartPolicy),
			ImagePullPolicy: models.ImagePullPolicy(a.ImagePullPolicy),
			RuntimeKind:     models.RuntimeKind(a.RuntimeKind),
			Replicas:        a.Replicas,
			// PlacementConstraints name node labels this platform may not have; they
			// are carried because a cluster that does have them wants them, and a
			// deploy that cannot satisfy one says so plainly.
			PlacementConstraints: a.PlacementConstraints,
			Metadata:             a.Metadata,
			Annotations:          a.Annotations,
			ContainerLabels:      a.ContainerLabels,
		}
		if id := stackIDs[a.Stack]; a.Stack != "" && id != 0 {
			in.StackID = &id
		}
		if id := registryIDs[a.Registry]; a.Registry != "" && id != 0 {
			in.RegistryID = &id
		}
		if id := repoIDs[a.GitRepository]; a.GitRepository != "" && id != 0 {
			in.GitRepositoryID = &id
		}
		for _, n := range a.Networks {
			if id := networkIDs[n]; id != 0 {
				in.NetworkIDs = append(in.NetworkIDs, id)
			}
		}
		for _, p := range a.Ports {
			in.Ports = append(in.Ports, application.PortSpec{
				ContainerPort: p.Container, Protocol: p.Protocol, Scheme: p.Scheme, Name: p.Name,
			})
		}

		app, err := r.svc.App.Create(r.target, in)
		if err != nil {
			r.add("app", a.Name, "failed", err.Error())
			continue
		}
		r.appIDs[a.Name] = app.ID
		r.createdApps = append(r.createdApps, a.Name)

		if err := r.finishApp(ctx, app, a); err != nil {
			r.add("app", a.Name, "failed", "created, but its configuration is incomplete: "+err.Error())
			continue
		}
		r.add("app", a.Name, "created", "")
	}
}

// finishApp writes the parts of an app's spec that Create does not take, and
// restores its mounts and environment.
func (r *restoreRun) finishApp(_ context.Context, app *models.Application, a wsbundle.Application) error {
	app.HealthcheckType = models.HealthcheckType(a.HealthcheckType)
	app.HealthcheckHTTPPath = a.HealthcheckHTTPPath
	app.HealthcheckPort = a.HealthcheckPort
	app.HealthcheckCommand = a.HealthcheckCommand
	if a.HealthcheckIntervalSeconds > 0 {
		app.HealthcheckIntervalSeconds = a.HealthcheckIntervalSeconds
	}
	if a.HealthcheckTimeoutSeconds > 0 {
		app.HealthcheckTimeoutSeconds = a.HealthcheckTimeoutSeconds
	}
	if a.HealthcheckRetries > 0 {
		app.HealthcheckRetries = a.HealthcheckRetries
	}
	app.HealthcheckStartPeriodSeconds = a.HealthcheckStartPeriodSeconds
	if a.DeployStrategy != "" {
		app.DeployStrategy = models.DeployStrategy(a.DeployStrategy)
	}
	// Mounts reference the volumes created earlier in this run, by the name the bundle used. A mount whose
	// volume did not come back is dropped rather than pointed at nothing — the app then starts without that
	// data instead of failing to start at all, and the report says which.
	for _, m := range a.Mounts {
		volID := r.volumeIDs[m.Volume]
		if volID == 0 {
			r.add("app", app.Name, "skipped", "mount at "+m.Path+" was dropped: volume "+m.Volume+" was not restored")
			continue
		}
		vol, err := r.svc.Volume.Get(r.target, volID)
		if err != nil {
			continue
		}
		app.Mounts = append(app.Mounts, models.AppMount{
			VolumeID: vol.ID, DockerName: vol.DockerName, Path: m.Path, ReadOnly: m.ReadOnly,
		})
	}
	if err := r.svc.App.Update(app); err != nil {
		return err
	}
	for _, e := range a.Env {
		if err := r.svc.App.SetEnvVar(app.ID, e.Key, e.Value, e.IsSecret); err != nil {
			logger.Warn("bundle: could not restore env var", "app", app.Name, "key", e.Key, "error", err)
		}
	}
	return nil
}
