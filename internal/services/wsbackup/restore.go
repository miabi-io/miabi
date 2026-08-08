// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package wsbackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dns"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
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
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// RestoreInput describes what to restore and where.
type RestoreInput struct {
	// Ref names the bundle in the bucket.
	Ref string
	// NewWorkspaceName, when set, restores into a workspace created for the
	// purpose instead of into the one asking. It is what makes a bundle a clone
	// rather than an overwrite.
	NewWorkspaceName string
	// RestoreData pulls the dumps and archives too.
	RestoreData bool
	// DeployApps rolls the restored applications out at the end.
	DeployApps bool
}

// Restore records a pending restore and schedules it. workspaceID is the workspace whose bucket and
// passphrase are used. The target is that same workspace unless NewWorkspaceName asks for a fresh one,
// created synchronously so a name clash is an error the caller sees rather than a job that fails later.
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
			// The workspace exists and the restore can still run against the source's
			// bucket; only its own future backups are unconfigured. Say so rather than
			// unwinding a workspace the operator can keep.
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

// inheritSettings copies one workspace's bundle target to another, so a restored
// clone points at the same bucket its bundle came from.
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
	_, err = s.Settings.Save(to, backupsettings.SaveInput{
		S3Enabled: true, S3Endpoint: cfg.Endpoint, S3Bucket: cfg.Bucket, S3Region: cfg.Region,
		S3AccessKey: cfg.AccessKey, S3SecretKey: &cfg.SecretKey,
		S3UseSSL: cfg.UseSSL, S3ForcePathStyle: cfg.ForcePathStyle,
		DatabaseBackupPath: src.DatabaseBackupPath, VolumeBackupPath: src.VolumeBackupPath,
		BundlePath: src.BundlePath, BundlePassphrase: &pass,
	})
	return err
}

// runRestore rebuilds a workspace from a bundle. Resources are created in dependency order — credentials,
// networks, secrets, volumes, stacks, databases, domains, certificates, apps, routing, delivery — and every one
// is keyed by name, so an existing resource is left as it is and reported skipped. Safe to run live, and twice.
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
	state, err := wsbundle.Open(sealed, passphrase)
	if err != nil {
		s.fail(b, err)
		return nil
	}

	target := b.TargetWorkspaceID
	if target == 0 {
		target = b.WorkspaceID
	}
	r := &restoreRun{
		svc: s, bundle: b, target: target, state: state, info: info,
		cfg: cfg, passphrase: passphrase, report: &models.BundleReport{},
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
		// Partial is the honest outcome: the resources that came back are real and
		// keeping them is the point, but a run reported as successful when a
		// database did not restore is how an operator finds out too late.
		b.Error = fmt.Sprintf("%d of %d resources could not be restored", len(fails), len(r.report.Items))
		s.fail(b, errors.New(b.Error))
		return nil
	}
	s.finish(b)
	return nil
}

type restoreRun struct {
	svc        *Service
	bundle     *models.WorkspaceBundle
	target     uint
	state      *wsbundle.State
	info       *wsbundle.Info
	cfg        *backup.S3Config
	passphrase string
	report     *models.BundleReport
	restored   int

	// appIDs and instanceIDs are the remap table: a natural key from the bundle to the id this platform minted for
	// it. Every cross-reference resolves through them, which is the whole reason nothing in a bundle carries an
	// id. They hold pre-existing resources too, so a reference resolves whether or not this run created it.
	appIDs      map[string]uint
	instanceIDs map[string]uint
	volumeIDs   map[string]uint
	// createdApps are the apps this run created, in bundle order — the only ones
	// it may deploy.
	createdApps []string
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

// steps is the restore's dependency order, named so it can be asserted rather than assumed. Each entry
// exists because something after it needs what it creates: domains before certificates, certificates
// before routing, apps before links/routing/delivery, and delivery last so GitOps sees a built workspace.
func (r *restoreRun) steps() []restoreStep {
	return []restoreStep{
		{"credentials", func(context.Context) { r.applyCredentials() }},
		{"networks", r.applyNetworks},
		{"secrets", func(context.Context) { r.applySecrets() }},
		{"volumes", r.applyVolumes},
		{"stacks", r.applyStacks},
		{"databases", r.applyDatabases},
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
		// No TestZone: connecting must not depend on a zone this platform can
		// reach right now. The credential is validated for shape, stored, and
		// tested by the operator (or by the first record it writes).
		if _, err := r.svc.DNSProvider.Connect(context.Background(), r.target, dnsprovider.ConnectInput{
			Name: p.Name, DisplayName: p.DisplayName, Type: p.Type, Credentials: creds,
		}); err != nil {
			r.add("dns-provider", p.Name, "failed", err.Error())
			continue
		}
		r.add("dns-provider", p.Name, "created", "")
	}
}

// applyDomains registers the workspace's owned hostnames. It runs BEFORE certificates, and that order is
// load-bearing: importing a certificate validates its subject names against registered domains, so one restored
// first is refused. Domains come back unverified — DNS at restore time still points wherever it pointed.
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
		// Re-link the DNS connection so ownership can be proven automatically,
		// rather than leaving the operator to paste a TXT record by hand.
		if id := providerIDs[d.DNSProvider]; d.DNSProvider != "" && id != 0 {
			if _, err := r.svc.Domain.SetDNSProvider(r.target, created.ID, &id); err != nil {
				logger.Warn("bundle: could not link the dns provider", "domain", d.Name, "error", err)
			}
		}
		r.add("domain", d.Name, "created", "ownership must be verified again on this platform")
	}
}

// applyCertificates re-imports the uploaded certificates. A Miabi-issued one is not recreated: issuing it
// here would ask a CA to validate a host that still resolves to the source, which fails — and would keep
// failing until DNS moves. It is reported instead, so the operator issues it as part of the cutover.
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

// applySecrets recreates the vault. An existing secret of the same name is never
// overwritten: the value in a bundle is as old as the bundle, and silently
// rolling back a rotated credential is worse than leaving one unrestored.
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
		if _, err := r.svc.Secret.Create(r.target, sec.Name, sec.Value, sec.Description, r.bundle.CreatedBy); err != nil {
			r.add("secret", sec.Name, "failed", err.Error())
			continue
		}
		r.add("secret", sec.Name, "created", "")
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
			// NFS/CIFS mount options and host paths belong to the node that serves
			// them; a bundle carries neither the password nor the guarantee that the
			// export exists here.
			r.add("volume", v.Name, "skipped",
				"driver "+driver+" is not portable; create it on the target and restore its archive by hand")
			continue
		}
		vol, err := r.svc.Volume.Create(ctx, r.target, 0, v.Name, v.SizeBytes, v.Metadata, v.Annotations)
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
			Metadata: k.Metadata, Annotations: k.Annotations,
		})
		if err != nil {
			r.add("stack", k.Name, "failed", err.Error())
			continue
		}
		for _, e := range k.Env {
			if err := r.svc.Stack.SetEnvVar(r.target, created.ID, e.Key, e.Value, e.IsSecret); err != nil {
				logger.Warn("bundle: could not restore stack env", "stack", k.Name, "key", e.Key, "error", err)
			}
		}
		r.add("stack", k.Name, "created", "")
	}
}

// applyDatabases provisions the managed servers and reserves their logical databases. Provision enqueues the
// container's bring-up rather than starting it, so each logical database is written pending, with its user and
// vault secrets, and bring-up runs the CREATE DDL once the engine answers — CREATE needs a running instance.
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
		inst, err := r.svc.Database.Provision(ctx, r.target, 0, d.Name,
			models.DBEngine(d.Engine), d.Version, d.VolumeSize, d.Metadata, d.Annotations)
		if err != nil {
			r.add("database", d.Name, "failed", err.Error())
			continue
		}
		r.instanceIDs[d.Name] = inst.ID
		r.add("database", d.Name, "created", "")

		if !models.EngineSupportsLogicalDatabases(inst.Engine) {
			// Redis has none; libSQL's single implicit database is created by
			// Provision itself. Either way there is nothing to reserve.
			continue
		}
		for _, ld := range d.Databases {
			if _, err := r.svc.Database.PrepareDatabase(r.target, inst.ID, ld.Name, nil); err != nil {
				r.add("database", d.Name+"/"+ld.Name, "failed", err.Error())
				continue
			}
			r.add("database", d.Name+"/"+ld.Name, "created", "")
		}
	}
}

// databaseWaitTimeout bounds how long the data phase waits for a freshly provisioned server to accept
// connections. Long enough to pull and start a database image on a cold host; short enough that a
// genuinely stuck bring-up is reported rather than hanging the run.
const databaseWaitTimeout = 5 * time.Minute

// waitForDatabase blocks until a reserved logical database has had its DDL applied by bring-up, or the wait runs
// out. It polls the row rather than the container: "running" on the logical database is the platform's own
// statement that the CREATE pair succeeded and the engine answered — strictly more than "the container started".
func (r *restoreRun) waitForDatabase(ctx context.Context, instanceID uint, name string) error {
	deadline := time.Now().Add(databaseWaitTimeout)
	for {
		// A server that failed to come up will never make its databases ready, so
		// say so at once instead of waiting out the deadline once per database.
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

// applyDatabaseLinks re-attaches each logical database to the app that owns it and re-injects the connection.
// The injection is the point: the app's environment came from a platform whose alias, port and generated user
// are not this one's. Re-injecting rewrites exactly those keys; a hand-typed connection string is left alone.
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
			if _, err := r.svc.Database.AttachToApp(r.target, db.ID, appID, ld.EnvPrefix); err != nil {
				r.add("database", d.Name+"/"+ld.Name, "failed", "could not attach to "+ld.App+": "+err.Error())
				continue
			}
			r.injectConnection(inst, db, appID, ld.EnvPrefix)
		}
	}
}

// injectConnection writes the target's own connection details onto the app,
// mirroring what attaching a database through the UI does.
func (r *restoreRun) injectConnection(inst *models.DatabaseInstance, db *models.Database, appID uint, prefix string) {
	conn, err := r.svc.Database.DatabaseConnection(inst, db)
	if err != nil {
		return
	}
	// Password and URL go in as Vault references, so the app's environment never
	// holds the plaintext and a rotation propagates to every consumer.
	passVal := "${{ secrets." + database.PasswordSecretName(inst, db) + " }}"
	uriVal := "${{ secrets." + database.URLSecretName(inst, db) + " }}"
	vars := []struct{ k, v string }{
		{"DATABASE_URL", uriVal},
		{"DB_HOST", conn.Host},
		{"DB_PORT", strconv.Itoa(conn.Port)},
		{"DB_NAME", conn.Database},
		{"DB_USER", conn.Username},
		{"DB_PASSWORD", passVal},
	}
	for _, v := range vars {
		if v.v == "" {
			continue
		}
		key := v.k
		if prefix != "" {
			key = prefix + "_" + v.k
		}
		if err := r.svc.App.SetEnvVar(appID, key, v.v, false); err != nil {
			logger.Warn("bundle: could not inject database env", "app", appID, "key", key, "error", err)
		}
	}
}

// applyRouting restores the middlewares and the routes that attach them. The domains those routes serve
// under were registered earlier (applyDomains), because the certificates between the two steps depend on
// them.
func (r *restoreRun) applyRouting(ctx context.Context) {
	// Middlewares first: a route names the ones it attaches, and one restored
	// without them would serve without the authentication or the rate limit it
	// was written to have.
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
		}
		detail := ""
		if rt.Certificate != "" {
			if id := certIDs[rt.Certificate]; id != 0 {
				in.CertificateID = &id
			} else {
				// The certificate did not travel (Miabi issues it) or did not import.
				// Serving custom TLS with no certificate is a route that cannot answer,
				// so it falls back to ACME and the report says so.
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

// applyDelivery restores what builds and reconciles the workspace: GitOps sources, promotion environments,
// pipeline definitions and schedules. It runs last, after the resources those act on exist — a GitOps source
// created before its apps would reconcile against an empty workspace and, with prune on, tear it down.
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
			SelfHeal: g.SelfHeal, AllowEmpty: g.AllowEmpty,
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
		if _, err := r.svc.Pipeline.Create(r.target, in); err != nil {
			r.add("pipeline", p.Name, "failed", err.Error())
			continue
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
			Name: c.Name, Schedule: c.Schedule, Command: c.Command, Entrypoint: c.Entrypoint,
			Image: c.Image, TimeoutSecs: c.TimeoutSecs, Enabled: c.Enabled,
			ConcurrencyPolicy: c.ConcurrencyPolicy, HistoryLimit: c.HistoryLimit,
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
}

// applyMembers links people the target already knows. It never creates users:
// authentication belongs to the platform, and a bundle that could mint accounts
// would be a way to grant access by uploading a file.
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
		// The server was provisioned a moment ago and comes up on the worker; its image may still be pulling.
		// Loading a dump into a database whose role does not exist yet fails in a way that reads like a bad dump,
		// so wait for the platform to say the database is ready first.
		if err := r.waitForDatabase(ctx, instID, target.Name); err != nil {
			r.add("database-data", name, "failed", err.Error())
			continue
		}
		err = r.svc.Backup.Restore(ctx, inst, target, backup.RestoreSpec{
			Filename:      art.File,
			Destination:   "s3",
			S3:            withPath(r.cfg, art.Path),
			S3Path:        art.Path,
			GPGPassphrase: r.passphrase,
		})
		if err != nil {
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
		if err := r.svc.restoreVolumeArchive(ctx, vol, r.cfg, art.Path, art.File, r.passphrase); err != nil {
			r.add("volume-data", art.Volume, "failed", err.Error())
			continue
		}
		r.add("volume-data", art.Volume, "created", "")
	}
}

// deploy rolls out the applications this run created. Only those: an app that was already here was left
// untouched, and redeploying it would restart a workload the operator never asked this run to touch.
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

// closingNotes records what a restore cannot do for the operator. They are the
// deliverable of the run: the difference between "it restored" and "it is
// serving" is DNS, certificates and tokens, none of which a bundle can carry.
func (r *restoreRun) closingNotes() {
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
