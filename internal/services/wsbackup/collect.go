// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// collect reads the live workspace into a bundle state document.
//
// Everything is keyed by name and nothing by id, because the install that reads
// this may share no id space with the one that wrote it. Where a reference cannot
// be expressed as a name — a privileged host-mount preset, an uploaded
// certificate, a node — it is left out and named in the report rather than
// carried as a number that means something else on the other side.
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
	if err := s.collectSecrets(workspaceID, st, report); err != nil {
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

// collectCredentials carries the registry and Git credentials apps depend on, so
// a private image still pulls and a private repo still clones on the target.
func (s *Service) collectCredentials(workspaceID uint, st *wsbundle.State, report *models.BundleReport) {
	regs, err := s.Registry.List(workspaceID)
	if err != nil {
		logger.Warn("bundle: list registries", "workspace", workspaceID, "error", err)
	}
	for i := range regs {
		r := &regs[i]
		// Read the secret through the service: listed records are stripped, and a
		// vault-backed credential exports as its reference (the Secret it names
		// travels in the same bundle).
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

// collectCertificates carries uploaded certificates with their material, and
// Miabi-issued ones as a declaration only — their key belongs to a host that
// still points at the source, so re-issuing is a cutover step, not an import one.
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

// collectDelivery carries the pieces that build and reconcile a workspace:
// pipeline definitions, GitOps sources, promotion environments and schedules.
// None of their history travels — a run, a sync and a job execution are records
// of something that happened on the platform it happened on.
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
			AllowEmpty: g.AllowEmpty, LastSyncedCommit: g.LastSyncedCommit,
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
		}
		if c.RegistryID != nil {
			entry.Registry = regName[*c.RegistryID]
		}
		st.CronJobs = append(st.CronJobs, entry)
		report.Add("cron-job", c.Name, "captured", "")
	}
}

// collectNetworks carries the workspace's own networks. The default network is
// skipped: every workspace has one, the target creates it itself, and carrying it
// would mean an import trying to create a second.
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

// collectSecrets carries the vault, values included — the one part of a bundle
// that makes the sealed state file non-negotiable.
//
// Managed secrets are skipped: they are minted and owned by a managed database,
// so the target creates its own with the credentials it generated. Carrying them
// would overwrite live credentials with a dead platform's.
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
			Value: val, Metadata: sec.Metadata,
		})
		report.Add("secret", sec.Name, "captured", "")
	}
	return nil
}

// collectStorage carries volume declarations. Their contents travel separately,
// as archives (see exportVolumes).
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
		// The shared environment travels with the stack: without it a member app
		// starts missing variables it never declared itself.
		vars, err := s.Stack.ListEnvVars(workspaceID, k.ID)
		if err != nil {
			report.Add("stack", k.Name, "failed", "environment could not be read: "+err.Error())
			continue
		}
		for j := range vars {
			v := vars[j]
			e, eErr := envEntry(v.Key, v.Value, v.IsSecret)
			if eErr != nil {
				// Drop the variable rather than carry ciphertext the target cannot
				// read: an app given an unreadable value fails in a way that looks
				// like a bug in the app.
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

// collectDatabases carries the managed servers and their logical databases,
// including which app each belongs to so the target can re-attach it.
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
			Version: inst.Version, VolumeSize: inst.VolumeSizeBytes,
			Metadata: inst.Metadata, Annotations: inst.Annotations,
		}
		dbs, err := s.Database.ListDatabases(workspaceID, inst.ID)
		if err != nil {
			return fmt.Errorf("list databases on %s: %w", inst.Name, err)
		}
		for j := range dbs {
			d := &dbs[j]
			ld := wsbundle.LogicalDatabase{Name: d.Name, EnvPrefix: d.EnvPrefix, Metadata: d.Metadata}
			if d.ApplicationID != nil {
				ld.App = appName[*d.ApplicationID]
			}
			entry.Databases = append(entry.Databases, ld)
		}
		st.Databases = append(st.Databases, entry)
	}
	return nil
}

// collectApps carries the workloads: their spec, their environment (secret values
// decrypted into the sealed file), their ports and their mounts.
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
			GPUCount: full.GPUCount, GPUKind: full.GPUKind,
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
			if m.VolumeID == 0 {
				// A privileged host bind: its source is an allow-listed preset on the
				// node that granted it, which may not exist — or may mean something
				// else — on the target.
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

// appEnv reads an app's environment with secret values decrypted. They land in
// the sealed state file and nowhere else; the target re-encrypts them under its
// own workspace key on restore.
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

// secretRef matches a `${{ secrets.NAME }}` reference — the same shape the vault
// resolves at deploy time (see services/secret).
var secretRef = regexp.MustCompile(`^\s*\$\{\{\s*secrets\.[A-Za-z][A-Za-z0-9_-]{0,62}\s*\}\}\s*$`)

// envEntry converts one stored environment variable into its bundle form.
//
// A variable that points at the vault is carried as the POINTER, never as what
// it resolves to. The secret it names travels in this same bundle, so resolving
// would write a second copy of it — and would cut the link that makes rotating
// the secret on the target reach every consumer. A reference is also not itself
// sensitive (it names a secret; it does not contain one), so it is carried in the
// clear and restored unencrypted, exactly as the platform writes its own.
//
// A value that is genuinely a literal secret is decrypted here, because the
// target has nowhere else to read it from. It never leaves the sealed state file.
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

// collectRouting carries routes and domains. Generated external-access routes are
// skipped: they belong to a port's exposure flag, and the target regenerates them
// under its own base domain.
func (s *Service) collectRouting(workspaceID uint, st *wsbundle.State, report *models.BundleReport) error {
	appName := map[uint]string{}
	apps, err := s.App.List(workspaceID)
	if err != nil {
		return fmt.Errorf("list applications: %w", err)
	}
	for i := range apps {
		appName[apps[i].ID] = apps[i].Name
	}

	// Middlewares first: a route names the ones it attaches, and a route restored
	// against a middleware that did not travel is a route serving without the
	// authentication or the rate limit it was written to have.
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

// collectMembers carries membership by email. Users are instance-global and
// authenticate against the platform they live on, so a bundle names people — it
// never carries the credentials that identify them.
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
