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
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// Keyed by name, never id: the reading install may share no id space; unnameable refs are reported.
func (s *Service) collect(workspaceID uint, report *models.BundleReport) (*wsbundle.State, error) {
	ws, err := s.Workspace.Get(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load workspace: %w", err)
	}
	st := &wsbundle.State{
		Schema: wsbundle.StateSchema,
		Source: wsbundle.Source{
			InstallID:    s.InstallID,
			MiabiVersion: s.Version,
			ExportedAt:   time.Now().UTC(),
		},
		Workspace: wsbundle.Workspace{
			Name:        ws.Name,
			DisplayName: ws.DisplayName,
			Description: ws.Description,
		},
	}

	s.collectCredentials(workspaceID, st, report)
	s.collectNetworks(workspaceID, st, report)
	if err := s.collectSealingKeys(workspaceID, st, report); err != nil {
		return nil, err
	}
	if err := s.collectSecrets(workspaceID, st, report); err != nil {
		return nil, err
	}
	if err := s.collectConfigs(workspaceID, st, report); err != nil {
		return nil, err
	}
	if err := s.collectStorage(workspaceID, st, report); err != nil {
		return nil, err
	}
	if err := s.collectDatabases(workspaceID, st, report); err != nil {
		return nil, err
	}
	s.collectCertificates(workspaceID, st, report)
	if err := s.collectApps(workspaceID, st, report); err != nil {
		return nil, err
	}
	if err := s.collectRouting(workspaceID, st, report); err != nil {
		return nil, err
	}
	s.collectDelivery(workspaceID, st, report)
	s.collectMembers(workspaceID, ws.OwnerID, st, report)
	return st, nil
}

func (s *Service) collectCredentials(workspaceID uint, st *wsbundle.State, report *models.BundleReport) {
	regs, err := s.Registry.List(workspaceID)
	if err != nil {
		logger.Warn("bundle: list registries", "workspace", workspaceID, "error", err)
	}
	for i := range regs {
		r := &regs[i]
		// Listed records are stripped; a vault-backed credential exports as its reference.
		sec, err := s.Registry.BundleSecret(workspaceID, r.ID)
		if err != nil {
			report.Add("registry", r.Name, "failed", "credential could not be read: "+err.Error())
			continue
		}
		st.Registries = append(st.Registries, wsbundle.Registry{
			Name: r.Name, DisplayName: r.DisplayName, Server: r.Server, Username: r.Username, Secret: sec,
		})
		report.Add("registry", r.Name, "captured", "")
	}

	repos, err := s.GitRepo.List(workspaceID)
	if err != nil {
		logger.Warn("bundle: list git credentials", "workspace", workspaceID, "error", err)
	}
	for i := range repos {
		r := &repos[i]
		sec, err := s.GitRepo.BundleSecret(workspaceID, r.ID)
		if err != nil {
			report.Add("git-credential", r.Name, "failed", "credential could not be read: "+err.Error())
			continue
		}
		st.GitRepos = append(st.GitRepos, wsbundle.GitRepository{
			Name: r.Name, DisplayName: r.DisplayName, URL: r.URL,
			AuthType: string(r.AuthType), Username: r.Username, Secret: sec,
		})
		report.Add("git-credential", r.Name, "captured", "")
	}

	providers, err := s.DNSProvider.List(workspaceID)
	if err != nil {
		logger.Warn("bundle: list dns providers", "workspace", workspaceID, "error", err)
	}
	for i := range providers {
		p := &providers[i]
		if p.ConsoleManaged() {
			report.Add("dns-provider", p.Name, "skipped", "managed by Miabi Console, which pushes it again")
			continue
		}
		creds, err := crypto.Decrypt(p.CredentialsEnc)
		if err != nil {
			report.Add("dns-provider", p.Name, "failed", "credential could not be decrypted: "+err.Error())
			continue
		}
		st.DNSProviders = append(st.DNSProviders, wsbundle.DNSProvider{
			Name: p.Name, DisplayName: p.DisplayName, Type: p.Type, Credentials: creds,
		})
		report.Add("dns-provider", p.Name, "captured", "")
	}
}

// Issued certs are declared only: their key belongs to a host still pointing at the source.
func (s *Service) collectCertificates(workspaceID uint, st *wsbundle.State, report *models.BundleReport) {
	providerName := map[uint]string{}
	if provs, err := s.DNSProvider.List(workspaceID); err == nil {
		for i := range provs {
			providerName[provs[i].ID] = provs[i].Name
		}
	}
	certs, err := s.Certificate.List(workspaceID)
	if err != nil {
		logger.Warn("bundle: list certificates", "workspace", workspaceID, "error", err)
		return
	}
	for i := range certs {
		c := &certs[i]
		entry := wsbundle.Certificate{
			Name: c.Name, DisplayName: c.DisplayName, Source: c.Source,
			DNSNames: c.DNSNames, AutoRenew: c.AutoRenew,
		}
		if c.DNSProviderID != nil {
			entry.DNSProvider = providerName[*c.DNSProviderID]
		}
		if c.Source == models.CertSourceACME {
			st.Certificates = append(st.Certificates, entry)
			report.Add("certificate", c.Name, "captured", "issued by Miabi; it re-issues on the target after DNS moves")
			continue
		}
		certPEM, keyPEM, err := s.Certificate.Resolve(workspaceID, c.ID)
		if err != nil {
			report.Add("certificate", c.Name, "failed", "material could not be read: "+err.Error())
			continue
		}
		entry.CertPEM, entry.KeyPEM = certPEM, keyPEM
		st.Certificates = append(st.Certificates, entry)
		report.Add("certificate", c.Name, "captured", "")
	}
}

// No history travels: runs, syncs and executions are records of the platform they happened on.
func (s *Service) collectDelivery(workspaceID uint, st *wsbundle.State, report *models.BundleReport) {
	appName := map[uint]string{}
	if apps, err := s.App.List(workspaceID); err == nil {
		for i := range apps {
			appName[apps[i].ID] = apps[i].Name
		}
	}
	repoName := map[uint]string{}
	if repos, err := s.GitRepo.List(workspaceID); err == nil {
		for i := range repos {
			repoName[repos[i].ID] = repos[i].Name
		}
	}
	regName := map[uint]string{}
	if regs, err := s.Registry.List(workspaceID); err == nil {
		for i := range regs {
			regName[regs[i].ID] = regs[i].Name
		}
	}

	sources, err := s.GitOps.List(workspaceID)
	if err != nil {
		logger.Warn("bundle: list gitops sources", "workspace", workspaceID, "error", err)
	}
	sourceName := map[uint]string{}
	for i := range sources {
		g := &sources[i]
		sourceName[g.ID] = g.Name
		entry := wsbundle.GitSource{
			Name: g.Name, DisplayName: g.DisplayName, RepoURL: g.RepoURL, Ref: g.Ref, Path: g.Path,
			SyncPolicy: string(g.SyncPolicy), Prune: g.Prune, SelfHeal: g.SelfHeal,
			AllowEmpty: g.AllowEmpty, RequireSealedSecrets: g.RequireSealedSecrets, LastSyncedCommit: g.LastCheckedCommit,
		}
		if g.GitRepositoryID != nil {
			entry.GitRepository = repoName[*g.GitRepositoryID]
		}
		st.GitSources = append(st.GitSources, entry)
		report.Add("gitops-source", g.Name, "captured", "")
	}

	envs, err := s.Environment.List(workspaceID)
	if err != nil {
		logger.Warn("bundle: list environments", "workspace", workspaceID, "error", err)
	}
	for i := range envs {
		e := &envs[i]
		entry := wsbundle.Environment{
			Name: e.Name, DisplayName: e.DisplayName, Description: e.Description,
			Rank: e.Rank, RequiredApprovals: e.RequiredApprovals,
		}
		if e.GitSourceID != nil {
			entry.GitSource = sourceName[*e.GitSourceID]
		}
		st.Environments = append(st.Environments, entry)
		report.Add("environment", e.Name, "captured", "")
	}

	pipelines, err := s.Pipeline.List(workspaceID)
	if err != nil {
		logger.Warn("bundle: list pipelines", "workspace", workspaceID, "error", err)
	}
	for i := range pipelines {
		p := &pipelines[i]
		entry := wsbundle.Pipeline{
			Name: p.Name, DisplayName: p.DisplayName, Spec: p.Spec, Enabled: p.Enabled,
			Source: string(p.Source), SourcePath: p.SourcePath, SourceRef: p.SourceRef,
		}
		if p.ApplicationID != nil {
			entry.App = appName[*p.ApplicationID]
		}
		if s.Versions != nil {
			if raw, err := s.Versions.Export(p); err != nil {
				logger.Warn("bundle: export version policy", "pipeline", p.Name, "error", err)
			} else {
				entry.Versioning = raw
			}
		}
		st.Pipelines = append(st.Pipelines, entry)
		report.Add("pipeline", p.Name, "captured", "")
	}

	crons, err := s.Jobs.ListCronJobs(workspaceID, 0)
	if err != nil {
		logger.Warn("bundle: list cron jobs", "workspace", workspaceID, "error", err)
	}
	for i := range crons {
		c := &crons[i]
		app := appName[c.ApplicationID]
		if app == "" {
			report.Add("cron-job", c.Name, "skipped", "its application is not in this workspace")
			continue
		}
		entry := wsbundle.CronJob{
			Name: c.Name, App: app, Schedule: c.Schedule, Command: c.Command,
			Entrypoint: c.Entrypoint, Image: c.Image, TimeoutSecs: c.TimeoutSecs,
			Enabled: c.Enabled, ConcurrencyPolicy: c.ConcurrencyPolicy, HistoryLimit: c.HistoryLimit,
			DisplayName: c.DisplayName, RunAsUser: c.RunAsUser, Metadata: c.Metadata, Annotations: c.Annotations,
		}
		if c.RegistryID != nil {
			entry.Registry = regName[*c.RegistryID]
		}
		st.CronJobs = append(st.CronJobs, entry)
		report.Add("cron-job", c.Name, "captured", "")
	}

	defs, err := s.Jobs.ListDefinitions(workspaceID, 0)
	if err != nil {
		logger.Warn("bundle: list job definitions", "workspace", workspaceID, "error", err)
	}
	for i := range defs {
		d := &defs[i]
		app := appName[d.ApplicationID]
		if app == "" {
			report.Add("job", d.Name, "skipped", "its application is not in this workspace")
			continue
		}
		entry := wsbundle.JobDefinition{
			Name: d.Name, App: app, Command: d.Command, Entrypoint: d.Entrypoint, Image: d.Image,
			RunAsUser: d.RunAsUser, TimeoutSecs: d.TimeoutSecs, RunPolicy: d.RunPolicy,
			WaitForDeploy: d.WaitForDeploy, HistoryLimit: d.HistoryLimit, BackoffLimit: d.BackoffLimit, SpecHash: d.SpecHash,
			Metadata: d.Metadata, Annotations: d.Annotations,
		}
		if d.RegistryID != nil {
			entry.Registry = regName[*d.RegistryID]
		}
		st.JobDefinitions = append(st.JobDefinitions, entry)
		report.Add("job", d.Name, "captured", "")
	}
}

// The target creates its own default network, so carrying it would create a second.
func (s *Service) collectNetworks(workspaceID uint, st *wsbundle.State, report *models.BundleReport) {
	nets, err := s.Network.List(workspaceID)
	if err != nil {
		logger.Warn("bundle: list networks", "workspace", workspaceID, "error", err)
		return
	}
	for i := range nets {
		n := &nets[i]
		if n.IsDefault || n.Imported {
			continue
		}
		st.Networks = append(st.Networks, wsbundle.Network{
			Name: n.Name, DisplayName: n.DisplayName, Driver: n.Driver, Internal: n.Internal,
		})
		report.Add("network", n.Name, "captured", "")
	}
}

// Without them every SealedSecret in the workspace's git repos stops opening on the target.
func (s *Service) collectSealingKeys(workspaceID uint, st *wsbundle.State, report *models.BundleReport) error {
	if s.Sealing == nil {
		return nil
	}
	keys, err := s.Sealing.Export(workspaceID)
	if err != nil {
		return fmt.Errorf("export sealing keys: %w", err)
	}
	for _, k := range keys {
		st.SealingKeys = append(st.SealingKeys, wsbundle.SealingKey{Version: k.Version, Identity: k.Identity, Active: k.Active, CreatedAt: k.CreatedAt})
		report.Add("sealing_key", fmt.Sprintf("v%d", k.Version), "captured", "")
	}
	return nil
}

// Managed secrets are skipped: the target mints its own, and carrying them would overwrite live credentials.
func (s *Service) collectSecrets(workspaceID uint, st *wsbundle.State, report *models.BundleReport) error {
	secrets, err := s.Secret.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list secrets: %w", err)
	}
	for i := range secrets {
		sec := &secrets[i]
		if sec.Managed {
			report.Add("secret", sec.Name, "skipped", "managed by "+sec.OwnerKind+"; recreated on restore")
			continue
		}
		val, err := s.Secret.Reveal(workspaceID, sec.ID)
		if err != nil {
			report.Add("secret", sec.Name, "failed", "value could not be decrypted: "+err.Error())
			continue
		}
		st.Secrets = append(st.Secrets, wsbundle.Secret{
			Name: sec.Name, DisplayName: sec.DisplayName, Description: sec.Description,
			Value: val, Metadata: sec.Metadata, SealedFP: sec.SealedFP, SealedKeyVersion: sec.SealedKeyVersion,
		})
		report.Add("secret", sec.Name, "captured", "")
	}
	return nil
}

// Without the files the apps mount, a restore looks right and does not run; managed configs are skipped.
func (s *Service) collectConfigs(workspaceID uint, st *wsbundle.State, report *models.BundleReport) error {
	if s.Config == nil {
		return nil
	}
	configs, err := s.Config.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list configs: %w", err)
	}
	for i := range configs {
		cfg := &configs[i]
		if cfg.Managed {
			report.Add("config", cfg.Name, "skipped", "managed by "+cfg.OwnerKind+"; recreated on restore")
			continue
		}
		data, err := s.Config.Data(cfg)
		if err != nil {
			report.Add("config", cfg.Name, "failed", "contents could not be decrypted: "+err.Error())
			continue
		}
		st.Configs = append(st.Configs, wsbundle.Config{
			Name: cfg.Name, DisplayName: cfg.DisplayName, Description: cfg.Description,
			Data: data, Mode: cfg.Mode, Sensitive: cfg.Sensitive,
			Delimiters: cfg.Delimiters, Metadata: cfg.Metadata,
		})
		report.Add("config", cfg.Name, "captured", "")
	}
	return nil
}

// Volume contents travel separately as archives (see exportVolumes).
func (s *Service) collectStorage(workspaceID uint, st *wsbundle.State, report *models.BundleReport) error {
	vols, err := s.Volume.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	for i := range vols {
		v := &vols[i]
		st.Volumes = append(st.Volumes, wsbundle.Volume{
			Name: v.Name, DisplayName: v.DisplayName, SizeBytes: v.SizeBytes,
			Driver: v.Driver, AccessMode: string(v.AccessMode), HostPath: v.HostPath,
			Metadata: v.Metadata, Annotations: v.Annotations,
		})
	}

	stacks, err := s.Stack.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list stacks: %w", err)
	}
	for i := range stacks {
		k := &stacks[i]
		entry := wsbundle.Stack{
			Name: k.Name, DisplayName: k.DisplayName, Description: k.Description,
			Metadata: k.Metadata, Annotations: k.Annotations,
		}
		// Without the shared environment a member app starts missing variables it never declared itself.
		vars, err := s.Stack.ListEnvVars(workspaceID, k.ID)
		if err != nil {
			report.Add("stack", k.Name, "failed", "environment could not be read: "+err.Error())
			continue
		}
		for j := range vars {
			v := vars[j]
			e, eErr := envEntry(v.Key, v.Value, v.IsSecret)
			if eErr != nil {

				report.Add("stack", k.Name+"/"+v.Key, "failed", "value could not be decrypted: "+eErr.Error())
				continue
			}
			entry.Env = append(entry.Env, e)
		}
		st.Stacks = append(st.Stacks, entry)
		report.Add("stack", k.Name, "captured", "")
	}
	return nil
}

func (s *Service) collectDatabases(workspaceID uint, st *wsbundle.State, report *models.BundleReport) error {
	instances, err := s.Database.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}
	appName := map[uint]string{}
	apps, err := s.App.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list applications: %w", err)
	}
	for i := range apps {
		appName[apps[i].ID] = apps[i].Name
	}

	for i := range instances {
		inst := &instances[i]
		entry := wsbundle.DatabaseInstance{
			Name: inst.Name, DisplayName: inst.DisplayName, Engine: string(inst.Engine),
			Version: inst.Version, VolumeSize: inst.VolumeSizeBytes, StorageClass: inst.StorageClassName,
			MemoryBytes: inst.MemoryBytes, NanoCPUs: inst.NanoCPUs,
			Metadata: inst.Metadata, Annotations: inst.Annotations,
		}
		dbs, err := s.Database.ListDatabases(workspaceID, inst.ID)
		if err != nil {
			return fmt.Errorf("list databases on %s: %w", inst.Name, err)
		}
		for j := range dbs {
			d := &dbs[j]
			ld := wsbundle.LogicalDatabase{Name: d.Name, EnvPrefix: d.EnvPrefix, EnvMap: d.EnvMap, Metadata: d.Metadata,
				ReadOnlyUser: d.ReadOnlyUsername != ""}
			if d.ApplicationID != nil {
				ld.App = appName[*d.ApplicationID]
				ld.AttachOnly = s.attachOnly(inst, d)
			}
			if conn, err := s.Database.DatabaseConnection(inst, d); err == nil {
				ld.Source = &wsbundle.Connection{
					Host: conn.Host, User: conn.Username, Password: conn.Password, URI: conn.URI,
					ReadOnlyUser: conn.ReadOnlyUsername, ReadOnlyPassword: conn.ReadOnlyPassword, ReadOnlyURI: conn.ReadOnlyURI,
				}
			}
			entry.Databases = append(entry.Databases, ld)
		}
		links, err := s.Database.ListInstanceLinks(inst.ID)
		if err != nil {
			return fmt.Errorf("list links of %s: %w", inst.Name, err)
		}
		for _, l := range links {
			if name := appName[l.ApplicationID]; name != "" {
				entry.Links = append(entry.Links, wsbundle.InstanceLink{App: name, EnvPrefix: l.EnvPrefix, EnvMap: l.EnvMap})
			}
		}
		st.Databases = append(st.Databases, entry)
	}
	return nil
}

// A link made before injections were recorded counts as injected only if the app holds every default variable.
func (s *Service) attachOnly(inst *models.DatabaseInstance, d *models.Database) bool {
	if len(d.EnvVars) > 0 {
		return false
	}
	vars, err := s.App.ListEnvVars(*d.ApplicationID)
	if err != nil {
		return false
	}
	have := make(map[string]bool, len(vars))
	for _, v := range vars {
		have[v.Key] = true
	}
	for _, k := range database.EnvKeys(inst.Engine, d.EnvPrefix, d.EnvMap) {
		if !have[k] {
			return true
		}
	}
	return false
}

// Secret env values are decrypted into the sealed state file.
func (s *Service) collectApps(workspaceID uint, st *wsbundle.State, report *models.BundleReport) error {
	apps, err := s.App.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list applications: %w", err)
	}
	volName := map[uint]string{}
	vols, err := s.Volume.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	for i := range vols {
		volName[vols[i].ID] = vols[i].Name
	}
	// Only configs the bundle carries resolve, so a skipped managed one is reported rather than silently dropped.
	cfgName := map[uint]string{}
	if s.Config != nil {
		configs, cerr := s.Config.List(workspaceID)
		if cerr != nil {
			return fmt.Errorf("list configs: %w", cerr)
		}
		for i := range configs {
			if !configs[i].Managed {
				cfgName[configs[i].ID] = configs[i].Name
			}
		}
	}
	stackName := map[uint]string{}
	stacks, err := s.Stack.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list stacks: %w", err)
	}
	for i := range stacks {
		stackName[stacks[i].ID] = stacks[i].Name
	}
	regName := map[uint]string{}
	if regs, rErr := s.Registry.List(workspaceID); rErr == nil {
		for i := range regs {
			regName[regs[i].ID] = regs[i].Name
		}
	}
	repoName := map[uint]string{}
	if repos, rErr := s.GitRepo.List(workspaceID); rErr == nil {
		for i := range repos {
			repoName[repos[i].ID] = repos[i].Name
		}
	}

	for i := range apps {
		full, err := s.App.Get(workspaceID, apps[i].ID)
		if err != nil {
			report.Add("app", apps[i].Name, "failed", "could not be read: "+err.Error())
			continue
		}
		a := wsbundle.Application{
			Name: full.Name, DisplayName: full.DisplayName, SourceType: string(full.SourceType),
			Icon: full.Icon, Image: full.Image, Tag: full.Tag,
			GitRepo: full.GitRepo, GitRef: full.GitRef, BuildMethod: string(full.BuildMethod),
			Builder: full.Builder, Buildpacks: full.Buildpacks, BuildEnv: full.BuildEnv,
			Command: full.Command, Port: full.Port,
			MemoryBytes: full.MemoryBytes, NanoCPUs: full.NanoCPUs,
			GPUCount: full.GPUCount, GPUKind: full.GPUKind, RunAsUser: full.RunAsUser,
			RestartPolicy: string(full.RestartPolicy), ImagePullPolicy: string(full.ImagePullPolicy),
			RuntimeKind: string(full.RuntimeKind), Replicas: full.Replicas,
			PlacementConstraints:          full.PlacementConstraints,
			HealthcheckType:               string(full.HealthcheckType),
			HealthcheckHTTPPath:           full.HealthcheckHTTPPath,
			HealthcheckPort:               full.HealthcheckPort,
			HealthcheckCommand:            full.HealthcheckCommand,
			HealthcheckIntervalSeconds:    full.HealthcheckIntervalSeconds,
			HealthcheckTimeoutSeconds:     full.HealthcheckTimeoutSeconds,
			HealthcheckRetries:            full.HealthcheckRetries,
			HealthcheckStartPeriodSeconds: full.HealthcheckStartPeriodSeconds,
			DeployStrategy:                string(full.DeployStrategy),
			ContainerLabels:               full.ContainerLabels,
			Metadata:                      full.Metadata,
			Annotations:                   full.Annotations,
		}
		if full.StackID != nil {
			a.Stack = stackName[*full.StackID]
		}
		if full.RegistryID != nil {
			a.Registry = regName[*full.RegistryID]
		}
		if full.GitRepositoryID != nil {
			a.GitRepository = repoName[*full.GitRepositoryID]
		}
		for _, n := range full.Networks {
			if n.IsDefault {
				continue // the target's own default network is attached for it
			}
			a.Networks = append(a.Networks, n.Name)
		}
		for _, p := range full.Ports {
			a.Ports = append(a.Ports, wsbundle.Port{
				Container: p.ContainerPort, Protocol: p.Protocol, Scheme: p.Scheme, Name: p.Name,
			})
		}
		for _, m := range full.Mounts {
			// Checked before the host-bind branch, which would otherwise drop it as an unportable host mount.
			if m.ConfigID != 0 {
				name := cfgName[m.ConfigID]
				if name == "" {
					report.Add("app", full.Name, "skipped",
						"config mounted at "+m.Path+" was not captured; re-attach it on the target")
					continue
				}
				a.Mounts = append(a.Mounts, wsbundle.Mount{
					Config: name, Key: m.ConfigKey, Path: m.Path, Mode: m.Mode, ReadOnly: m.ReadOnly,
				})
				continue
			}
			if m.VolumeID == 0 {
				// A host bind's source is a preset on the granting node, which may not exist or differ on the target.
				report.Add("app", full.Name, "skipped",
					"host mount at "+m.Path+" is not portable; re-create it on the target")
				continue
			}
			name := volName[m.VolumeID]
			if name == "" {
				continue
			}
			a.Mounts = append(a.Mounts, wsbundle.Mount{Volume: name, Path: m.Path, ReadOnly: m.ReadOnly})
		}
		env, err := s.appEnv(full)
		if err != nil {
			report.Add("app", full.Name, "failed", "environment could not be read: "+err.Error())
			continue
		}
		a.Env = env
		st.Apps = append(st.Apps, a)
		report.Add("app", full.Name, "captured", "")
	}
	return nil
}

// Decrypted values land only in the sealed state file; the target re-encrypts them under its own key.
func (s *Service) appEnv(app *models.Application) ([]wsbundle.EnvVar, error) {
	vars, err := s.Apps.ListEnvVars(app.ID)
	if err != nil {
		return nil, err
	}
	out := make([]wsbundle.EnvVar, 0, len(vars))
	for i := range vars {
		entry, err := envEntry(vars[i].Key, vars[i].Value, vars[i].IsSecret)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

var secretRef = regexp.MustCompile(`^\s*\$\{\{\s*secrets\.[A-Za-z][A-Za-z0-9_-]{0,62}\s*\}\}\s*$`)

// A vault reference travels as the pointer: resolving would duplicate the secret and cut the rotation link.
func envEntry(key, stored string, isSecret bool) (wsbundle.EnvVar, error) {
	value := stored
	if isSecret {
		plain, err := crypto.Decrypt(stored)
		if err != nil {
			return wsbundle.EnvVar{}, fmt.Errorf("%s: %w", key, err)
		}
		value = plain
	}
	if secretRef.MatchString(value) {
		return wsbundle.EnvVar{Key: key, Value: strings.TrimSpace(value)}, nil
	}
	return wsbundle.EnvVar{Key: key, Value: value, IsSecret: isSecret}, nil
}

// Generated routes are skipped: the target regenerates them under its own base domain.
func (s *Service) collectRouting(workspaceID uint, st *wsbundle.State, report *models.BundleReport) error {
	appName := map[uint]string{}
	apps, err := s.App.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list applications: %w", err)
	}
	for i := range apps {
		appName[apps[i].ID] = apps[i].Name
	}

	// A route restored without its middlewares would serve without its authentication or rate limit.
	middlewares, err := s.Middleware.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list middlewares: %w", err)
	}
	for i := range middlewares {
		m := &middlewares[i]
		st.Middlewares = append(st.Middlewares, wsbundle.Middleware{
			Name: m.Name, DisplayName: m.DisplayName, Type: m.Type,
			Paths: m.Paths, Rule: m.Rule,
		})
		report.Add("middleware", m.Name, "captured", "")
	}

	certName := map[uint]string{}
	if certs, cErr := s.Certificate.List(workspaceID); cErr == nil {
		for i := range certs {
			certName[certs[i].ID] = certs[i].Name
		}
	}

	routes, err := s.Route.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list routes: %w", err)
	}
	for i := range routes {
		r := &routes[i]
		if r.Generated {
			continue
		}
		app := appName[r.ApplicationID]
		if app == "" {
			report.Add("route", r.Name, "skipped", "its application is not in this workspace")
			continue
		}
		entry := wsbundle.Route{
			Name: r.Name, DisplayName: r.DisplayName, App: app, Hosts: r.Hosts, Path: r.Path,
			Methods: r.Methods, Middlewares: r.Middlewares, Rewrite: r.Rewrite,
			TargetPort: r.TargetPort, TLSMode: string(r.TLSMode), Enabled: r.Enabled,
			ExploitProtection: r.ExploitProtection,
		}
		if r.Maintenance.Enabled {
			entry.Maintenance = &wsbundle.RouteMaintenance{
				Enabled: true, StatusCode: r.Maintenance.StatusCode, Message: r.Maintenance.Message,
			}
		}
		if r.CertificateID != nil {
			entry.Certificate = certName[*r.CertificateID]
		}
		st.Routes = append(st.Routes, entry)
		if r.TLSMode == models.RouteTLSCustom && entry.Certificate == "" {
			report.Add("route", r.Name, "captured",
				"serves custom TLS but names no stored certificate; upload one on the target")
		} else {
			report.Add("route", r.Name, "captured", "")
		}
	}

	domains, err := s.Domain.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	provName := map[uint]string{}
	if provs, pErr := s.DNSProvider.List(workspaceID); pErr == nil {
		for i := range provs {
			provName[provs[i].ID] = provs[i].Name
		}
	}
	for i := range domains {
		d := &domains[i]
		entry := wsbundle.Domain{
			Name: d.Name, TLSMode: string(d.TLSMode), Wildcard: d.Wildcard,
		}
		if d.DNSProviderID != nil {
			entry.DNSProvider = provName[*d.DNSProviderID]
		}
		st.Domains = append(st.Domains, entry)
	}
	return nil
}

// Users are instance-global, so a bundle names people by email and never carries credentials.
func (s *Service) collectMembers(workspaceID, ownerID uint, st *wsbundle.State, report *models.BundleReport) {
	members, err := s.Workspace.ListMembers(workspaceID)
	if err != nil {
		logger.Warn("bundle: list members", "workspace", workspaceID, "error", err)
		return
	}
	for i := range members {
		m := &members[i]
		email := m.User.Email
		if email == "" {
			continue
		}
		st.Members = append(st.Members, wsbundle.Member{
			Email: email, Role: string(m.Role), Owner: m.UserID == ownerID,
		})
	}
}
