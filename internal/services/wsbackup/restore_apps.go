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

package wsbackup

import (
	"context"
	"errors"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// Goes through the application service so apps get this platform's alias, placement, quota and naming.
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
			RunAsUser:       a.RunAsUser,
			RestartPolicy:   models.RestartPolicy(a.RestartPolicy),
			ImagePullPolicy: models.ImagePullPolicy(a.ImagePullPolicy),
			RuntimeKind:     models.RuntimeKind(a.RuntimeKind),
			Replicas:        a.Replicas,
			// Carried because a cluster with those labels wants them; an unsatisfiable deploy says so plainly.
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

		in.ServerID = r.at.ServerID
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

func (r *restoreRun) finishApp(_ context.Context, app *models.Application, a wsbundle.Application) error {
	app.HealthcheckType = restoredHealthcheckType(a.HealthcheckType, r.state.Schema)
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

	for _, m := range a.Mounts {

		if m.Config != "" {
			cfg, err := r.configByName(m.Config)
			if err != nil {
				r.add("app", app.Name, "skipped",
					"config mount at "+m.Path+" was dropped: config "+m.Config+" was not restored")
				continue
			}
			app.Mounts = append(app.Mounts, models.AppMount{
				ConfigID: cfg.ID, ConfigKey: m.Key, Path: m.Path, Mode: m.Mode, ReadOnly: m.ReadOnly,
			})
			continue
		}
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
		if err := r.svc.App.SetEnvVar(app.ID, e.Key, r.rewrite(e.Value), e.IsSecret); err != nil {
			logger.Warn("bundle: could not restore env var", "app", app.Name, "key", e.Key, "error", err)
		}
	}
	return nil
}

// A mount may point at a config that already existed on the target and was skipped, not created.
func (r *restoreRun) configByName(name string) (*models.Config, error) {
	if r.svc.Config == nil {
		return nil, errConfigsUnavailable
	}
	return r.svc.Config.GetByName(r.target, name)
}

// errConfigsUnavailable reports an install with no config service wired.
var errConfigsUnavailable = errors.New("configs are not available on this install")

// Before schema 3, "none" meant "keep the image's own check", which is "image" now.
func restoredHealthcheckType(t string, schema int) models.HealthcheckType {
	if models.HealthcheckType(t) == models.HealthcheckNone && schema < wsbundle.HealthcheckNoneDisables {
		return models.HealthcheckImage
	}
	return models.HealthcheckType(t)
}
