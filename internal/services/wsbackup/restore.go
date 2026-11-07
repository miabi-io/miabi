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
	"encoding/json"
	"errors"
	"fmt"
	"github.com/miabi-io/miabi/internal/services/placement"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dns"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	configsvc "github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/dnsprovider"
	"github.com/miabi-io/miabi/internal/services/domain"
	"github.com/miabi-io/miabi/internal/services/environment"
	"github.com/miabi-io/miabi/internal/services/gitops"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/services/job"
	"github.com/miabi-io/miabi/internal/services/middleware"
	"github.com/miabi-io/miabi/internal/services/network"
	"github.com/miabi-io/miabi/internal/services/pipeline"
	"github.com/miabi-io/miabi/internal/services/registry"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/sealing"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// RestoreInput describes what to restore and where.
type RestoreInput struct {
	// Ref names the bundle in the bucket.
	Ref string
	// A fresh workspace makes a bundle a clone rather than an overwrite.
	NewWorkspaceName string
	// RestoreData pulls the dumps and archives too.
	RestoreData bool
	// DeployApps rolls the restored applications out at the end.
	DeployApps bool
}

// A new workspace is created synchronously so a name clash is an error now, not a failed job later.
func (s *Service) Restore(ctx context.Context, workspaceID uint, userID *uint, in RestoreInput) (*models.WorkspaceBundle, error) {
	cfg, prefix, _, err := s.Settings.BundleTarget(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	info, err := s.FindBundle(ctx, workspaceID, in.Ref)
	if err != nil {
		return nil, err
	}

	target := workspaceID
	if name := strings.TrimSpace(in.NewWorkspaceName); name != "" {
		if userID == nil {
			return nil, errors.New("creating a workspace requires a user to own it")
		}
		ws, err := s.Workspace.Create(*userID, name, name, "Restored from "+info.Ref)
		if err != nil {
			return nil, err
		}
		if err := s.inheritSettings(workspaceID, ws.ID); err != nil {
			// The workspace is keepable and the restore still works; only its own future backups are unconfigured.
			logger.Warn("bundle: could not copy backup settings to the restored workspace",
				"workspace", ws.ID, "error", err)
		}
		target = ws.ID
	}

	return s.start(ctx, &models.WorkspaceBundle{
		WorkspaceID:       workspaceID,
		TargetWorkspaceID: target,
		Kind:              models.BundleRestore,
		Ref:               info.Ref,
		Status:            models.BackupPending,
		Trigger:           "manual",
		RestoreData:       in.RestoreData,
		DeployApps:        in.DeployApps,
		S3Bucket:          cfg.Bucket,
		S3Prefix:          prefix,
		SourceWorkspace:   info.Workspace,
		CreatedBy:         userID,
	})
}

func (s *Service) inheritSettings(from, to uint) error {
	src, err := s.Settings.Get(from)
	if err != nil {
		return err
	}
	cfg, err := s.Settings.S3ConfigFor(from)
	if err != nil {
		return err
	}
	if cfg == nil {
		return backupsettings.ErrS3NotConfigured
	}
	_, _, pass, err := s.Settings.BundleTarget(from)
	if err != nil {
		return err
	}
	var bundlePass *string
	if pass != "" {
		bundlePass = &pass
	}
	_, err = s.Settings.Save(to, backupsettings.SaveInput{
		S3Enabled: true, S3Endpoint: cfg.Endpoint, S3Bucket: cfg.Bucket, S3Region: cfg.Region,
		S3AccessKey: cfg.AccessKey, S3SecretKey: &cfg.SecretKey,
		S3UseSSL: cfg.UseSSL, S3ForcePathStyle: cfg.ForcePathStyle,
		DatabaseBackupPath: src.DatabaseBackupPath, VolumeBackupPath: src.VolumeBackupPath,
		BundlePath: src.BundlePath, BundlePassphrase: bundlePass,
	})
	return err
}

// Dependency-ordered and keyed by name, so existing resources are skipped; safe to run live, and twice.
func (s *Service) runRestore(ctx context.Context, b *models.WorkspaceBundle) error {
	cfg, prefix, passphrase, err := s.Settings.BundleTarget(b.WorkspaceID)
	if err != nil {
		s.fail(b, fmt.Errorf("%w: %v", ErrNotConfigured, err))
		return nil
	}
	store, err := s.store(cfg)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	infoBody, err := store.GetBytes(ctx, wsbundle.InfoObject(prefix, b.Ref))
	if err != nil {
		s.fail(b, fmt.Errorf("read bundle info: %w", err))
		return nil
	}
	info, err := wsbundle.DecodeInfo(infoBody)
	if err != nil {
		s.fail(b, err)
		return nil
	}
	sealed, err := store.GetBytes(ctx, info.StateArtifact().Key())
	if err != nil {
		s.fail(b, fmt.Errorf("read bundle state: %w", err))
		return nil
	}
	state, err := wsbundle.Decode(sealed, passphrase)
	if err != nil {
		s.fail(b, err)
		return nil
	}

	s.restoreFrom(ctx, b, state, info, bucketSource{cfg: cfg, passphrase: passphrase}, nil)
	return nil
}

// report is extended rather than restarted, so a migration's transfer items come first.
func (s *Service) restoreFrom(ctx context.Context, b *models.WorkspaceBundle, state *wsbundle.State, info *wsbundle.Info, src artifactSource, report *models.BundleReport) *restoreRun {
	target := b.TargetWorkspaceID
	if target == 0 {
		target = b.WorkspaceID
	}
	if report == nil {
		report = &models.BundleReport{}
	}
	r := &restoreRun{svc: s, bundle: b, target: target, state: state, info: info, src: src, report: report}
	if s.Placer != nil {
		// Otherwise a workspace with no usable location restores half its resources before one fails to place.
		at, err := s.Placer.Place(placement.Request{WorkspaceID: target})
		if err != nil {
			b.Report = *r.report
			s.fail(b, fmt.Errorf("no location to restore into: %w", err))
			return r
		}
		r.at = at
	}

	s.phase(b, models.BundlePhaseState)
	r.apply(ctx)

	if b.RestoreData {
		s.phase(b, models.BundlePhaseDatabases)
		r.restoreDatabases(ctx)
		s.phase(b, models.BundlePhaseVolumes)
		r.restoreVolumes(ctx)
	} else {
		r.report.Note("Data was not restored: only the workspace's configuration was rebuilt.")
	}

	if b.DeployApps {
		r.deploy()
	} else if len(state.Apps) > 0 {
		r.report.Note("Applications were created but not deployed. Deploy them when the target is ready to serve.")
	}

	r.closingNotes()
	b.Report = *r.report
	b.Artifacts = r.restored
	if fails := r.report.Failures(); len(fails) > 0 {
		// Reporting success when a database didn't restore is how an operator finds out too late.
		s.partial(b, fmt.Errorf("%d of %d resources could not be restored", len(fails), len(r.report.Items)))
		return r
	}
	s.finish(b)
	return r
}

type artifactSource interface {
	restoreDatabase(ctx context.Context, r *restoreRun, art wsbundle.Artifact, inst *models.DatabaseInstance, db *models.Database) error
	restoreVolume(ctx context.Context, r *restoreRun, art wsbundle.Artifact, vol *models.Volume) error
}

// bucketSource reads a bundle's artifacts from its S3 bucket.
type bucketSource struct {
	cfg        *backup.S3Config
	passphrase string
}

// None for a cleartext artifact, even when the workspace has a passphrase now.
func (b bucketSource) passphraseFor(art wsbundle.Artifact) string {
	if !art.Encrypted {
		return ""
	}
	return b.passphrase
}

func (b bucketSource) restoreDatabase(ctx context.Context, r *restoreRun, art wsbundle.Artifact, inst *models.DatabaseInstance, db *models.Database) error {
	return r.svc.Backup.Restore(ctx, inst, db, backup.RestoreSpec{
		Filename:      art.File,
		Destination:   "s3",
		S3:            withPath(b.cfg, art.Path),
		S3Path:        art.Path,
		GPGPassphrase: b.passphraseFor(art),
	})
}

func (b bucketSource) restoreVolume(ctx context.Context, r *restoreRun, art wsbundle.Artifact, vol *models.Volume) error {
	return r.svc.restoreVolumeArchive(ctx, vol, b.cfg, art.Path, art.File, b.passphraseFor(art))
}

type restoreRun struct {
	svc      *Service
	bundle   *models.WorkspaceBundle
	target   uint
	state    *wsbundle.State
	info     *wsbundle.Info
	src      artifactSource
	report   *models.BundleReport
	restored int
	// One node, so apps can mount the volumes and reach the databases restored with them.
	at placement.Result

	// Every cross-reference resolves through these, which is why no bundle carries an id; includes pre-existing ones.
	appIDs      map[string]uint
	instanceIDs map[string]uint
	volumeIDs   map[string]uint
	// The only apps this run may deploy.
	createdApps []string
	// conn rewrites source database connection values to the target's; nil until mapConnections runs.
	conn      *strings.Replacer
	rewritten int
}

func (r *restoreRun) add(kind, name, action, detail string) {
	if action == "created" {
		r.restored++
	}
	r.report.Add(kind, name, action, detail)
}

// apply creates the configuration graph in dependency order.
func (r *restoreRun) apply(ctx context.Context) {
	r.appIDs = map[string]uint{}
	r.instanceIDs = map[string]uint{}
	r.volumeIDs = map[string]uint{}

	for _, step := range r.steps() {
		step.run(ctx)
	}
}

type restoreStep struct {
	name string
	run  func(context.Context)
}

// Named so the order can be asserted; delivery runs last so GitOps sees a built workspace.
func (r *restoreRun) steps() []restoreStep {
	return []restoreStep{
		// Before secrets: a restored GitOps source re-applies sealed values, which must open on the target.
		{"sealing-keys", func(context.Context) { r.applySealingKeys() }},
		// Before credentials: a registry or Git credential may name a vault secret instead of holding its token.
		{"secrets", func(context.Context) { r.applySecrets() }},
		{"credentials", func(context.Context) { r.applyCredentials() }},
		{"networks", r.applyNetworks},
		{"volumes", r.applyVolumes},
		{"databases", r.applyDatabases},
		// After databases, before anything storing env or files, so source connection values get rewritten.
		{"connections", func(context.Context) { r.mapConnections() }},
		// An app's config mount can only be rebuilt once the config it names exists.
		{"configs", func(context.Context) { r.applyConfigs() }},
		{"stacks", r.applyStacks},
		{"domains", func(context.Context) { r.applyDomains() }},
		{"certificates", func(context.Context) { r.applyCertificates() }},
		{"apps", r.applyApps},
		{"database-links", func(context.Context) { r.applyDatabaseLinks() }},
		{"routing", r.applyRouting},
		{"delivery", func(context.Context) { r.applyDelivery() }},
		{"members", func(context.Context) { r.applyMembers() }},
	}
}

func (r *restoreRun) applyCredentials() {
	existing := map[string]bool{}
	if regs, err := r.svc.Registry.List(r.target); err == nil {
		for i := range regs {
			existing[regs[i].Name] = true
		}
	}
	for _, reg := range r.state.Registries {
		if existing[reg.Name] {
			r.add("registry", reg.Name, "skipped", "already exists")
			continue
		}
		if _, err := r.svc.Registry.Create(r.target, registry.Input{
			Name: reg.Name, Server: reg.Server, Username: reg.Username, Secret: reg.Secret,
		}); err != nil {
			r.add("registry", reg.Name, "failed", err.Error())
			continue
		}
		r.add("registry", reg.Name, "created", "")
	}

	existingRepos := map[string]bool{}
	if repos, err := r.svc.GitRepo.List(r.target); err == nil {
		for i := range repos {
			existingRepos[repos[i].Name] = true
		}
	}
	for _, repo := range r.state.GitRepos {
		if existingRepos[repo.Name] {
			r.add("git-credential", repo.Name, "skipped", "already exists")
			continue
		}
		if _, err := r.svc.GitRepo.Create(r.target, gitrepo.Input{
			Name: repo.Name, DisplayName: repo.DisplayName, URL: repo.URL,
			AuthType: models.GitAuthType(repo.AuthType), Username: repo.Username, Secret: repo.Secret,
		}); err != nil {
			r.add("git-credential", repo.Name, "failed", err.Error())
			continue
		}
		r.add("git-credential", repo.Name, "created", "")
	}

	existingProviders := map[string]bool{}
	if provs, err := r.svc.DNSProvider.List(r.target); err == nil {
		for i := range provs {
			existingProviders[provs[i].Name] = true
		}
	}
	for _, p := range r.state.DNSProviders {
		if existingProviders[p.Name] {
			r.add("dns-provider", p.Name, "skipped", "already exists")
			continue
		}
		var creds dns.Credentials
		if err := json.Unmarshal([]byte(p.Credentials), &creds); err != nil {
			r.add("dns-provider", p.Name, "failed", "credential could not be read: "+err.Error())
			continue
		}
		// No TestZone: connecting must not depend on a zone this platform can reach right now.
		if _, err := r.svc.DNSProvider.Connect(context.Background(), r.target, dnsprovider.ConnectInput{
			Name: p.Name, DisplayName: p.DisplayName, Type: p.Type, Credentials: creds,
		}); err != nil {
			r.add("dns-provider", p.Name, "failed", err.Error())
			continue
		}
		r.add("dns-provider", p.Name, "created", "")
	}
}

// Before certificates: importing a cert validates its names against registered domains; domains come back unverified.
func (r *restoreRun) applyDomains() {
	providerIDs := map[string]uint{}
	if provs, err := r.svc.DNSProvider.List(r.target); err == nil {
		for i := range provs {
			providerIDs[provs[i].Name] = provs[i].ID
		}
	}
	existing := map[string]bool{}
	if domains, err := r.svc.Domain.List(r.target); err == nil {
		for i := range domains {
			existing[domains[i].Name] = true
		}
	}
	for _, d := range r.state.Domains {
		if existing[d.Name] {
			r.add("domain", d.Name, "skipped", "already exists")
			continue
		}
		created, err := r.svc.Domain.Create(r.target, domain.Input{
			Name: d.Name, TLSMode: models.DomainTLSMode(d.TLSMode), Wildcard: d.Wildcard,
		})
		if err != nil {
			r.add("domain", d.Name, "failed", err.Error())
			continue
		}
		// Re-linked so ownership can be proven automatically rather than by a hand-pasted TXT record.
		if id := providerIDs[d.DNSProvider]; d.DNSProvider != "" && id != 0 {
			if _, err := r.svc.Domain.SetDNSProvider(r.target, created.ID, &id); err != nil {
				logger.Warn("bundle: could not link the dns provider", "domain", d.Name, "error", err)
			}
		}
		r.add("domain", d.Name, "created", "ownership must be verified again on this platform")
	}
}

// Issuing here would make a CA validate a host still resolving to the source, so it's left for cutover.
func (r *restoreRun) applyCertificates() {
	existing := map[string]bool{}
	if certs, err := r.svc.Certificate.List(r.target); err == nil {
		for i := range certs {
			existing[certs[i].Name] = true
		}
	}
	for _, c := range r.state.Certificates {
		if existing[c.Name] {
			r.add("certificate", c.Name, "skipped", "already exists")
			continue
		}
		if c.CertPEM == "" || c.KeyPEM == "" {
			r.add("certificate", c.Name, "skipped",
				"Miabi issues this certificate; re-issue it here once DNS points at this platform")
			continue
		}
		if _, err := r.svc.Certificate.Import(r.target, c.Name, c.DisplayName, c.CertPEM, c.KeyPEM); err != nil {
			r.add("certificate", c.Name, "failed", err.Error())
			continue
		}
		r.add("certificate", c.Name, "created", "")
	}
}

func (r *restoreRun) applyNetworks(ctx context.Context) {
	existing := map[string]bool{}
	if nets, err := r.svc.Network.List(r.target); err == nil {
		for i := range nets {
			existing[nets[i].Name] = true
		}
	}
	for _, n := range r.state.Networks {
		if existing[n.Name] {
			r.add("network", n.Name, "skipped", "already exists")
			continue
		}
		if _, err := r.svc.Network.Create(ctx, r.target, network.Input{
			Name: n.Name, DisplayName: n.DisplayName, Driver: n.Driver, Internal: n.Internal,
		}); err != nil {
			r.add("network", n.Name, "failed", err.Error())
			continue
		}
		r.add("network", n.Name, "created", "")
	}
}

// Existing secrets are never overwritten: silently rolling back a rotated credential is worse.
func (r *restoreRun) applySecrets() {
	existing := map[string]bool{}
	if secrets, err := r.svc.Secret.List(r.target); err == nil {
		for i := range secrets {
			existing[secrets[i].Name] = true
		}
	}
	for _, sec := range r.state.Secrets {
		if existing[sec.Name] {
			r.add("secret", sec.Name, "skipped", "already exists; its current value was kept")
			continue
		}
		created, err := r.svc.Secret.Create(r.target, sec.Name, sec.Value, sec.Description, r.bundle.CreatedBy)
		if err != nil {
			r.add("secret", sec.Name, "failed", err.Error())
			continue
		}
		if sec.SealedFP != "" {
			if _, err := r.svc.Secret.ApplySealed(r.target, created.Name, sec.Value, sec.SealedFP, sec.SealedKeyVersion); err != nil {
				r.add("secret", sec.Name, "created", "sealed-value fingerprint not kept: "+err.Error())
				continue
			}
		}
		r.add("secret", sec.Name, "created", "")
	}
}

// A target with keys keeps its active one and gains the imported ones as older versions.
func (r *restoreRun) applySealingKeys() {
	if len(r.state.SealingKeys) == 0 {
		return
	}
	if r.svc.Sealing == nil {
		r.add("sealing_key", "all", "skipped", "sealed secrets are not available on this install")
		return
	}
	keys := make([]sealing.ExportedKey, 0, len(r.state.SealingKeys))
	for _, k := range r.state.SealingKeys {
		keys = append(keys, sealing.ExportedKey{Version: k.Version, Identity: k.Identity, Active: k.Active, CreatedAt: k.CreatedAt})
	}
	n, err := r.svc.Sealing.Import(r.target, keys)
	if err != nil {
		r.add("sealing_key", "all", "failed", err.Error())
		return
	}
	r.restored += n
	r.add("sealing_key", "all", "imported", fmt.Sprintf("%d of %d keys added", n, len(keys)))
}

func (r *restoreRun) applyConfigs() {
	if r.svc.Config == nil {
		for _, cfg := range r.state.Configs {
			r.add("config", cfg.Name, "skipped", "configs are not available on this install")
		}
		return
	}
	existing := map[string]bool{}
	if configs, err := r.svc.Config.List(r.target); err == nil {
		for i := range configs {
			existing[configs[i].Name] = true
		}
	}
	for _, cfg := range r.state.Configs {
		if existing[cfg.Name] {
			r.add("config", cfg.Name, "skipped", "already exists; its current contents were kept")
			continue
		}
		_, err := r.svc.Config.Create(r.target, configsvc.Input{
			Name: cfg.Name, DisplayName: cfg.DisplayName, Description: cfg.Description,
			Data: r.rewriteData(cfg.Data), Mode: cfg.Mode, Sensitive: cfg.Sensitive, Delimiters: cfg.Delimiters,
		}, r.bundle.CreatedBy)
		if err != nil {
			r.add("config", cfg.Name, "failed", err.Error())
			continue
		}
		r.add("config", cfg.Name, "created", "")
	}
}

func (r *restoreRun) applyVolumes(ctx context.Context) {
	existing := map[string]*models.Volume{}
	if vols, err := r.svc.Volume.List(r.target); err == nil {
		for i := range vols {
			existing[vols[i].Name] = &vols[i]
		}
	}
	for _, v := range r.state.Volumes {
		if cur, ok := existing[v.Name]; ok {
			r.volumeIDs[v.Name] = cur.ID
			r.add("volume", v.Name, "skipped", "already exists")
			continue
		}
		driver := v.Driver
		if driver == "" {
			driver = models.VolumeDriverLocal
		}
		if driver != models.VolumeDriverLocal {
			r.add("volume", v.Name, "skipped",
				"driver "+driver+" is not portable; create it on the target and restore its archive by hand")
			continue
		}
		vol, err := r.svc.Volume.Create(ctx, r.target, r.at.ServerID, v.Name, v.SizeBytes, v.Metadata, v.Annotations)
		if err != nil {
			r.add("volume", v.Name, "failed", err.Error())
			continue
		}
		r.volumeIDs[v.Name] = vol.ID
		r.add("volume", v.Name, "created", "")
	}
}

func (r *restoreRun) applyStacks(ctx context.Context) {
	existing := map[string]bool{}
	if stacks, err := r.svc.Stack.List(r.target); err == nil {
		for i := range stacks {
			existing[stacks[i].Name] = true
		}
	}
	for _, k := range r.state.Stacks {
		if existing[k.Name] {
			r.add("stack", k.Name, "skipped", "already exists")
			continue
		}
		created, err := r.svc.Stack.Create(ctx, r.target, stack.Input{
			Name: k.Name, DisplayName: k.DisplayName, Description: k.Description,
			Metadata: k.Metadata, Annotations: k.Annotations, ClusterID: r.at.ClusterID,
		})
		if err != nil {
			r.add("stack", k.Name, "failed", err.Error())
			continue
		}
		for _, e := range k.Env {
			if _, err := r.svc.Stack.SetEnvVar(r.target, created.ID, e.Key, r.rewrite(e.Value), e.IsSecret); err != nil {
				logger.Warn("bundle: could not restore stack env", "stack", k.Name, "key", e.Key, "error", err)
			}
		}
		r.add("stack", k.Name, "created", "")
	}
}

// CREATE needs a running instance, so logical databases are written pending and bring-up runs the DDL.
func (r *restoreRun) applyDatabases(ctx context.Context) {
	existing := map[string]*models.DatabaseInstance{}
	if instances, err := r.svc.Database.List(r.target); err == nil {
		for i := range instances {
			existing[instances[i].Name] = &instances[i]
		}
	}
	for _, d := range r.state.Databases {
		if cur, ok := existing[d.Name]; ok {
			r.instanceIDs[d.Name] = cur.ID
			r.add("database", d.Name, "skipped", "already exists")
			continue
		}
		inst, err := r.svc.Database.Provision(ctx, r.target, r.at.ServerID, d.Name,
			models.DBEngine(d.Engine), d.Version, d.VolumeSize, d.StorageClass,
			database.Resources{MemoryBytes: d.MemoryBytes, NanoCPUs: d.NanoCPUs}, d.Metadata, d.Annotations)
		if err != nil {
			r.add("database", d.Name, "failed", err.Error())
			continue
		}
		r.instanceIDs[d.Name] = inst.ID
		r.add("database", d.Name, "created", "")

		if !models.EngineSupportsLogicalDatabases(inst.Engine) {
			// Redis has none and libSQL's single implicit database is created by Provision itself.
			continue
		}
		for _, ld := range d.Databases {
			created, err := r.svc.Database.PrepareDatabase(r.target, inst.ID, ld.Name, nil)
			if err != nil {
				r.add("database", d.Name+"/"+ld.Name, "failed", err.Error())
				continue
			}
			// Reserved now so its grants cover the tables the data restore loads.
			if ld.ReadOnlyUser {
				if err := r.svc.Database.EnableReadOnly(ctx, created); err != nil {
					r.add("database", d.Name+"/"+ld.Name, "failed", "read-only user: "+err.Error())
					continue
				}
			}
			r.add("database", d.Name+"/"+ld.Name, "created", "")
		}
	}
}

// Long enough to pull and start a database image on a cold host; short enough to report a stuck bring-up.
const databaseWaitTimeout = 5 * time.Minute

// Polls the row, not the container: "running" there means the CREATE pair succeeded and the engine answered.
func (r *restoreRun) waitForDatabase(ctx context.Context, instanceID uint, name string) error {
	deadline := time.Now().Add(databaseWaitTimeout)
	for {
		// A failed server will never get ready, so fail at once instead of waiting out each database's deadline.
		if inst, err := r.svc.Database.Get(r.target, instanceID); err == nil && inst.Status == models.DBStatusFailed {
			return fmt.Errorf("the database server failed to start")
		}
		dbs, err := r.svc.Database.ListDatabases(r.target, instanceID)
		if err != nil {
			return err
		}
		for i := range dbs {
			if dbs[i].Name != name {
				continue
			}
			switch dbs[i].Status {
			case models.DBStatusRunning:
				return nil
			case models.DBStatusFailed:
				return fmt.Errorf("the database failed to provision")
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the database was still provisioning after %s; restore its dump once the server is up",
				databaseWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// The env came from another platform's alias, port and user; only those keys are rewritten, hand-typed ones kept.
func (r *restoreRun) applyDatabaseLinks() {
	for _, d := range r.state.Databases {
		instID := r.instanceIDs[d.Name]
		if instID == 0 {
			continue
		}
		inst, err := r.svc.Database.Get(r.target, instID)
		if err != nil {
			continue
		}
		live, err := r.svc.Database.ListDatabases(r.target, instID)
		if err != nil {
			continue
		}
		byName := map[string]*models.Database{}
		for i := range live {
			byName[live[i].Name] = &live[i]
		}
		for _, ld := range d.Databases {
			db := byName[ld.Name]
			if db == nil || ld.App == "" {
				continue
			}
			appID := r.appIDs[ld.App]
			if appID == 0 {
				continue
			}
			if ld.AttachOnly {
				if _, err := r.svc.Database.AttachToApp(r.target, db.ID, appID, database.EnvLink{}); err != nil {
					r.add("database", d.Name+"/"+ld.Name, "failed", "could not attach to "+ld.App+": "+err.Error())
				}
				continue
			}
			env := r.envLink(inst, d.Name+"/"+ld.Name, ld.EnvPrefix, ld.EnvMap)
			if _, err := r.svc.Database.AttachToApp(r.target, db.ID, appID, env); err != nil {
				r.add("database", d.Name+"/"+ld.Name, "failed", "could not attach to "+ld.App+": "+err.Error())
				continue
			}
			r.injectConnection(inst, db, appID, env)
		}
		for _, l := range d.Links {
			appID := r.appIDs[l.App]
			if appID == 0 {
				continue
			}
			env := r.envLink(inst, d.Name, l.EnvPrefix, l.EnvMap)
			if _, err := r.svc.Database.LinkInstance(r.target, instID, appID, env); err != nil {
				r.add("database", d.Name, "failed", "could not link to "+l.App+": "+err.Error())
				continue
			}
			r.injectConnection(inst, nil, appID, env)
		}
	}
}

// A mapping this platform rejects falls back to the defaults, so the link still lands.
func (r *restoreRun) envLink(inst *models.DatabaseInstance, what, prefix string, m map[string]string) database.EnvLink {
	norm, err := database.NormalizeEnvMap(inst.Engine, prefix, m)
	if err != nil {
		r.report.Note(what + ": env mapping ignored, defaults used: " + err.Error())
		norm = nil
	}
	return database.EnvLink{Prefix: prefix, Map: norm, Vars: database.EnvKeys(inst.Engine, prefix, norm)}
}

// db is nil for an instance link.
func (r *restoreRun) injectConnection(inst *models.DatabaseInstance, db *models.Database, appID uint, env database.EnvLink) {
	vars, err := r.svc.Database.AppEnv(inst, db, env.Prefix, env.Map)
	if err != nil {
		return
	}
	for _, v := range vars {
		if err := r.svc.App.SetEnvVar(appID, v.Key, v.Value, v.Secret); err != nil {
			logger.Warn("bundle: could not inject database env", "app", appID, "key", v.Key, "error", err)
		}
	}
}

func (r *restoreRun) applyRouting(ctx context.Context) {
	// A route restored without its middlewares would serve without its authentication or rate limit.
	existingMW := map[string]bool{}
	if mws, err := r.svc.Middleware.List(r.target); err == nil {
		for i := range mws {
			existingMW[mws[i].Name] = true
		}
	}
	for _, m := range r.state.Middlewares {
		if existingMW[m.Name] {
			r.add("middleware", m.Name, "skipped", "already exists")
			continue
		}
		if _, err := r.svc.Middleware.Create(ctx, r.target, middleware.Input{
			Name: m.Name, DisplayName: m.DisplayName, Type: m.Type, Paths: m.Paths, Rule: m.Rule,
		}); err != nil {
			r.add("middleware", m.Name, "failed", err.Error())
			continue
		}
		r.add("middleware", m.Name, "created", "")
	}

	certIDs := map[string]uint{}
	if certs, err := r.svc.Certificate.List(r.target); err == nil {
		for i := range certs {
			certIDs[certs[i].Name] = certs[i].ID
		}
	}

	existingRoutes := map[string]bool{}
	if routes, err := r.svc.Route.List(r.target); err == nil {
		for i := range routes {
			existingRoutes[routes[i].Name] = true
		}
	}
	for _, rt := range r.state.Routes {
		if existingRoutes[rt.Name] {
			r.add("route", rt.Name, "skipped", "already exists")
			continue
		}
		appID := r.appIDs[rt.App]
		if appID == 0 {
			r.add("route", rt.Name, "failed", "its application "+rt.App+" was not restored")
			continue
		}
		enabled := rt.Enabled
		in := route.Input{
			Name: rt.Name, DisplayName: rt.DisplayName, ApplicationID: appID,
			Path: rt.Path, Hosts: rt.Hosts, Methods: rt.Methods, Middlewares: rt.Middlewares,
			Rewrite: rt.Rewrite, TargetPort: rt.TargetPort,
			TLSMode: models.RouteTLSMode(rt.TLSMode), Enabled: &enabled,
			ExploitProtection: &rt.ExploitProtection,
		}
		if rt.Maintenance != nil {
			in.Maintenance = &models.RouteMaintenance{
				Enabled: rt.Maintenance.Enabled, StatusCode: rt.Maintenance.StatusCode, Message: rt.Maintenance.Message,
			}
		}
		detail := ""
		if rt.Certificate != "" {
			if id := certIDs[rt.Certificate]; id != 0 {
				in.CertificateID = &id
			} else {
				// Custom TLS with no certificate can't answer, so the route falls back to ACME and the report says so.
				in.TLSMode = models.RouteTLSACME
				detail = "certificate " + rt.Certificate + " is not here yet; the route was set to ACME TLS"
			}
		}
		if _, err := r.svc.Route.Create(ctx, r.target, in); err != nil {
			r.add("route", rt.Name, "failed", err.Error())
			continue
		}
		r.add("route", rt.Name, "created", detail)
	}
}

// Runs last: a GitOps source created before its apps would reconcile an empty workspace and, with prune, tear it down.
func (r *restoreRun) applyDelivery() {
	repoIDs := map[string]uint{}
	if repos, err := r.svc.GitRepo.List(r.target); err == nil {
		for i := range repos {
			repoIDs[repos[i].Name] = repos[i].ID
		}
	}
	existingSources := map[string]bool{}
	if sources, err := r.svc.GitOps.List(r.target); err == nil {
		for i := range sources {
			existingSources[sources[i].Name] = true
		}
	}
	sourceIDs := map[string]uint{}
	for _, g := range r.state.GitSources {
		if existingSources[g.Name] {
			r.add("gitops-source", g.Name, "skipped", "already exists")
			continue
		}
		in := gitops.Input{
			Name: g.Name, DisplayName: g.DisplayName, RepoURL: g.RepoURL, Ref: g.Ref, Path: g.Path,
			SyncPolicy: models.GitSyncPolicy(g.SyncPolicy), Prune: g.Prune,
			SelfHeal: g.SelfHeal, AllowEmpty: g.AllowEmpty, RequireSealedSecrets: g.RequireSealedSecrets,
		}
		if id := repoIDs[g.GitRepository]; g.GitRepository != "" && id != 0 {
			in.GitRepositoryID = &id
		}
		created, err := r.svc.GitOps.Create(r.target, in)
		if err != nil {
			r.add("gitops-source", g.Name, "failed", err.Error())
			continue
		}
		sourceIDs[g.Name] = created.ID
		r.add("gitops-source", g.Name, "created", "a new webhook secret was generated; re-point the provider's webhook at this platform")
	}

	existingEnvs := map[string]bool{}
	if envs, err := r.svc.Environment.List(r.target); err == nil {
		for i := range envs {
			existingEnvs[envs[i].Name] = true
		}
	}
	for _, e := range r.state.Environments {
		if existingEnvs[e.Name] {
			r.add("environment", e.Name, "skipped", "already exists")
			continue
		}
		in := environment.Input{
			Name: e.Name, DisplayName: e.DisplayName, Description: e.Description,
			Rank: e.Rank, RequiredApprovals: e.RequiredApprovals,
		}
		if id := sourceIDs[e.GitSource]; e.GitSource != "" && id != 0 {
			in.GitSourceID = &id
		}
		if _, err := r.svc.Environment.Create(r.target, in); err != nil {
			r.add("environment", e.Name, "failed", err.Error())
			continue
		}
		r.add("environment", e.Name, "created", "")
	}

	existingPipelines := map[string]bool{}
	if defs, err := r.svc.Pipeline.List(r.target); err == nil {
		for i := range defs {
			existingPipelines[defs[i].Name] = true
		}
	}
	for _, p := range r.state.Pipelines {
		if existingPipelines[p.Name] {
			r.add("pipeline", p.Name, "skipped", "already exists")
			continue
		}
		enabled := p.Enabled
		in := pipeline.Input{
			Name: p.Name, DisplayName: p.DisplayName, Spec: p.Spec, Enabled: &enabled,
			Source: models.PipelineSource(p.Source), SourcePath: p.SourcePath, SourceRef: p.SourceRef,
		}
		if id := r.appIDs[p.App]; p.App != "" && id != 0 {
			in.ApplicationID = &id
			in.SetApplicationID = true
		}
		def, err := r.svc.Pipeline.Create(r.target, in)
		if err != nil {
			r.add("pipeline", p.Name, "failed", err.Error())
			continue
		}
		if r.svc.Versions != nil && len(p.Versioning) > 0 {
			if err := r.svc.Versions.Import(def, p.Versioning); err != nil {
				r.add("pipeline", p.Name, "created", "version policy not restored: "+err.Error())
				continue
			}
		}
		r.add("pipeline", p.Name, "created", "")
	}

	regIDs := map[string]uint{}
	if regs, err := r.svc.Registry.List(r.target); err == nil {
		for i := range regs {
			regIDs[regs[i].Name] = regs[i].ID
		}
	}
	existingCrons := map[string]bool{}
	if crons, err := r.svc.Jobs.ListCronJobs(r.target, 0); err == nil {
		for i := range crons {
			existingCrons[crons[i].Name] = true
		}
	}
	for _, c := range r.state.CronJobs {
		if existingCrons[c.Name] {
			r.add("cron-job", c.Name, "skipped", "already exists")
			continue
		}
		appID := r.appIDs[c.App]
		if appID == 0 {
			r.add("cron-job", c.Name, "failed", "its application "+c.App+" was not restored")
			continue
		}
		in := job.CronJobInput{
			Name: c.Name, DisplayName: c.DisplayName, Schedule: c.Schedule, Command: c.Command, Entrypoint: c.Entrypoint,
			Image: c.Image, RunAsUser: c.RunAsUser, TimeoutSecs: c.TimeoutSecs, Enabled: c.Enabled,
			ConcurrencyPolicy: c.ConcurrencyPolicy, HistoryLimit: c.HistoryLimit,
			Metadata: c.Metadata, Annotations: c.Annotations,
		}
		if id := regIDs[c.Registry]; c.Registry != "" && id != 0 {
			in.RegistryID = &id
		}
		if _, err := r.svc.Jobs.CreateCronJob(r.target, appID, in); err != nil {
			r.add("cron-job", c.Name, "failed", err.Error())
			continue
		}
		r.add("cron-job", c.Name, "created", "")
	}

	// Not run: they ran on the source, and their fingerprint keeps the next GitOps sync from rerunning them.
	for _, d := range r.state.JobDefinitions {
		if _, err := r.svc.Jobs.GetDefinitionByName(r.target, d.Name); err == nil {
			r.add("job", d.Name, "skipped", "already exists")
			continue
		}
		appID := r.appIDs[d.App]
		if appID == 0 {
			r.add("job", d.Name, "failed", "its application "+d.App+" was not restored")
			continue
		}
		in := job.DefinitionInput{
			Name: d.Name, ApplicationID: appID, Command: d.Command, Entrypoint: d.Entrypoint, Image: d.Image,
			RunAsUser: d.RunAsUser, TimeoutSecs: d.TimeoutSecs, RunPolicy: d.RunPolicy,
			WaitForDeploy: d.WaitForDeploy, HistoryLimit: d.HistoryLimit, BackoffLimit: d.BackoffLimit, SpecHash: d.SpecHash,
			Metadata: d.Metadata, Annotations: d.Annotations,
		}
		if id := regIDs[d.Registry]; d.Registry != "" && id != 0 {
			in.RegistryID = &id
		}
		if _, _, err := r.svc.Jobs.SaveDefinition(context.Background(), r.target, in, false); err != nil {
			r.add("job", d.Name, "failed", err.Error())
			continue
		}
		r.add("job", d.Name, "created", "")
	}
}

// Never creates users: a bundle that could mint accounts would grant access by uploading a file.
func (r *restoreRun) applyMembers() {
	for _, m := range r.state.Members {
		user, err := r.svc.Users.FindByEmail(m.Email)
		if err != nil || user == nil {
			r.add("member", m.Email, "skipped", "no user with that email on this platform; invite them")
			continue
		}
		if cur, _ := r.svc.Workspaces.FindMember(r.target, user.ID); cur != nil {
			r.add("member", m.Email, "skipped", "already a member")
			continue
		}
		role := models.WorkspaceRole(m.Role)
		if !role.Valid() {
			role = models.WorkspaceRoleViewer
		}
		if err := r.svc.Workspaces.AddMember(&models.WorkspaceMember{
			WorkspaceID: r.target, UserID: user.ID, Role: role,
		}); err != nil {
			r.add("member", m.Email, "failed", err.Error())
			continue
		}
		r.add("member", m.Email, "created", string(role))
	}
}

func (r *restoreRun) restoreDatabases(ctx context.Context) {
	for _, art := range r.info.BySubject(wsbundle.SubjectDatabase) {
		name := art.Instance + "/" + art.Database
		instID := r.instanceIDs[art.Instance]
		if instID == 0 {
			r.add("database-data", name, "skipped", "its server was not restored")
			continue
		}
		inst, err := r.svc.Database.Get(r.target, instID)
		if err != nil {
			r.add("database-data", name, "failed", err.Error())
			continue
		}
		dbs, err := r.svc.Database.ListDatabases(r.target, instID)
		if err != nil {
			r.add("database-data", name, "failed", err.Error())
			continue
		}
		var target *models.Database
		for i := range dbs {
			if dbs[i].Name == art.Database {
				target = &dbs[i]
				break
			}
		}
		if target == nil {
			r.add("database-data", name, "skipped", "no database of that name on the restored server")
			continue
		}
		// Loading a dump before the role exists fails like a bad dump, so wait until the database is ready.
		if err := r.waitForDatabase(ctx, instID, target.Name); err != nil {
			r.add("database-data", name, "failed", err.Error())
			continue
		}
		if err := r.src.restoreDatabase(ctx, r, art, inst, target); err != nil {
			r.add("database-data", name, "failed", err.Error())
			continue
		}
		r.add("database-data", name, "created", "")
	}
}

func (r *restoreRun) restoreVolumes(ctx context.Context) {
	live := map[string]*models.Volume{}
	if vols, err := r.svc.Volume.List(r.target); err == nil {
		for i := range vols {
			live[vols[i].Name] = &vols[i]
		}
	}
	for _, art := range r.info.BySubject(wsbundle.SubjectVolume) {
		vol := live[art.Volume]
		if vol == nil {
			r.add("volume-data", art.Volume, "skipped", "the volume was not restored")
			continue
		}
		if err := r.src.restoreVolume(ctx, r, art, vol); err != nil {
			r.add("volume-data", art.Volume, "failed", err.Error())
			continue
		}
		r.add("volume-data", art.Volume, "created", "")
	}
}

// Only created apps: redeploying a pre-existing one would restart a workload nobody asked this run to touch.
func (r *restoreRun) deploy() {
	for _, name := range r.createdApps {
		app, err := r.svc.App.Get(r.target, r.appIDs[name])
		if err != nil {
			continue
		}
		if _, err := r.svc.App.Deploy(app, nil, "", app.DeployStrategy); err != nil {
			r.add("app-deploy", name, "failed", err.Error())
			continue
		}
		r.add("app-deploy", name, "created", "")
	}
}

// The gap between "restored" and "serving" is DNS, certificates and tokens, none of which a bundle carries.
func (r *restoreRun) closingNotes() {
	r.connectionNote()
	if len(r.state.Domains) > 0 {
		r.report.Note("Domains were restored unverified. Point DNS at this platform and verify each domain before its routes serve traffic.")
	}
	for _, c := range r.state.Certificates {
		if c.CertPEM == "" {
			r.report.Note("Certificate " + c.Name + " is issued by Miabi: re-issue it here once DNS resolves to this platform. Its routes serve ACME TLS until then.")
		}
	}
	if len(r.state.GitSources) > 0 {
		r.report.Note("GitOps sources were restored with a new webhook secret each. Re-point the provider's webhook at this platform, and expect the next sweep to reconcile them.")
	}
	if len(r.state.DNSProviders) > 0 {
		r.report.Note("DNS connections were restored but not tested. Test each one before relying on it to prove domain ownership.")
	}
	if len(r.state.Databases) > 0 && r.bundle.DeployApps {
		r.report.Note("Database servers come up in the background. An app deployed by this restore may start before its database answers and restart until it does.")
	}
	r.report.Note("API tokens do not travel. Re-issue any token your automation used against the source workspace.")
	if r.target != r.bundle.WorkspaceID {
		r.report.Note("This restore created a separate workspace: it does not take over the source's hostnames until you move DNS.")
	}
}
