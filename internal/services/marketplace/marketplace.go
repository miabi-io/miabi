// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package marketplace

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	configsvc "github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrTemplateNotFound = errors.New("template not found")
	ErrMissingInput     = errors.New("missing required input")
	ErrInvalidInput     = errors.New("invalid input value")
	// ErrNoSharedInstance aliases the database package's sentinel (where placement
	// resolution lives) so errors.Is checks keep matching.
	ErrNoSharedInstance = database.ErrNoSharedInstance
	ErrInvalidTemplate  = errors.New("invalid template")
)

// TemplateStore is the DB-backed catalog of user-imported templates. The
// embedded official catalog is served separately from memory.
type TemplateStore interface {
	EnsureCustomSource(workspaceID uint) (*models.TemplateSource, error)
	UpsertTemplate(t *models.Template) error
	ListCustom(workspaceID uint) ([]models.Template, error)
	FindCustom(workspaceID uint, slug, version string) (*models.Template, error)
	DeleteCustom(workspaceID uint, slug string) (int64, error)
}

// Service installs marketplace templates by orchestrating the application,
// database, storage and stack services.
type Service struct {
	apps      *application.Service
	dbs       *database.Service
	volumes   *storage.Service
	stacks    *stack.Service
	installs  *repositories.TemplateInstallRepository
	templates TemplateStore

	// configs creates the template's configuration files; nil leaves a template
	// declaring configs to fail validation rather than install half of itself.
	configs *configsvc.Service

	// remote is the synced official+community registry (the marketplace service
	// export bundle), merged into the catalog. nil = embedded-only (no sync).
	remote RemoteCatalog

	// Live install-progress jobs (single-node, in-process), streamed over the
	// event bus to the install UI. bus may be nil in tests / when unwired.
	bus    *eventbus.Bus
	jobsMu sync.Mutex
	jobs   map[string]*InstallJob
}

// NewService wires the install collaborators. volumes, stacks, installs and
// templates may be nil in tests that only exercise the embedded catalog.
// SetConfigs wires the config service used to materialize a template's files.
func (s *Service) SetConfigs(c *configsvc.Service) { s.configs = c }

func NewService(apps *application.Service, dbs *database.Service, volumes *storage.Service, stacks *stack.Service, installs *repositories.TemplateInstallRepository, templates TemplateStore) *Service {
	return &Service{apps: apps, dbs: dbs, volumes: volumes, stacks: stacks, installs: installs, templates: templates, jobs: map[string]*InstallJob{}}
}

// SetEventBus wires the in-process bus used to stream live install progress over
// SSE. Optional: without it, installs still run, just without live progress.
func (s *Service) SetEventBus(b *eventbus.Bus) { s.bus = b }

// Import validates a user-supplied manifest and stores it as a custom template
// in the workspace (a re-import of the same version replaces it). The new
// catalog entry is returned.
func (s *Service) Import(workspaceID uint, rawYAML string) (CatalogEntry, error) {
	return s.storeCustom(workspaceID, rawYAML)
}

// UpdateCustom replaces an existing custom template (identified by slug) with a
// new manifest. The slug must not change — editing is in-place per template;
// changing the version simply adds/replaces that version, matching Import.
func (s *Service) UpdateCustom(workspaceID uint, slug, rawYAML string) (CatalogEntry, error) {
	if s.templates == nil {
		return CatalogEntry{}, ErrInvalidTemplate
	}
	existing, _ := s.templates.FindCustom(workspaceID, slug, "")
	if existing == nil {
		return CatalogEntry{}, ErrTemplateNotFound
	}
	m, err := manifest.Parse([]byte(rawYAML))
	if err != nil {
		return CatalogEntry{}, fmt.Errorf("%w: %v", ErrInvalidTemplate, err)
	}
	if m.Metadata.Name != slug {
		return CatalogEntry{}, fmt.Errorf("%w: slug cannot be changed (was %q, got %q)", ErrInvalidTemplate, slug, m.Metadata.Name)
	}
	return s.storeCustom(workspaceID, rawYAML)
}

// GetCustomYAML returns the stored manifest of a custom template for editing
// (empty version = highest). Official templates are not editable, so only the
// workspace's custom source is consulted.
func (s *Service) GetCustomYAML(workspaceID uint, slug, version string) (string, bool) {
	if s.templates == nil {
		return "", false
	}
	t, _ := s.templates.FindCustom(workspaceID, slug, version)
	if t == nil {
		return "", false
	}
	return t.RawYAML, true
}

// DeleteCustom removes a custom template (all its versions) from the workspace.
func (s *Service) DeleteCustom(workspaceID uint, slug string) error {
	if s.templates == nil {
		return ErrTemplateNotFound
	}
	n, err := s.templates.DeleteCustom(workspaceID, slug)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTemplateNotFound
	}
	return nil
}

func (s *Service) storeCustom(workspaceID uint, rawYAML string) (CatalogEntry, error) {
	if s.templates == nil {
		return CatalogEntry{}, ErrInvalidTemplate
	}
	m, err := manifest.Parse([]byte(rawYAML))
	if err != nil {
		return CatalogEntry{}, fmt.Errorf("%w: %v", ErrInvalidTemplate, err)
	}
	src, err := s.templates.EnsureCustomSource(workspaceID)
	if err != nil {
		return CatalogEntry{}, err
	}
	t := &models.Template{
		SourceID: src.ID, Name: m.Metadata.Name, Version: m.Metadata.Version,
		DisplayName: m.Metadata.DisplayName, Category: m.Metadata.Category, Icon: m.Metadata.Icon,
		Digest: manifest.Digest([]byte(rawYAML)), RawYAML: rawYAML,
	}
	if err := s.templates.UpsertTemplate(t); err != nil {
		return CatalogEntry{}, err
	}
	return entryFromManifest(m, SourceCustom, nil), nil
}

// ListForWorkspace returns the merged catalog visible to the workspace: the embedded official floor
// unioned with the synced official+community registry, plus the workspace's imported custom templates.
// The Source field on each entry (official | community | custom) drives the UI's three tabs.
func (s *Service) ListForWorkspace(workspaceID uint) ([]CatalogEntry, error) {
	out := s.officialAndCommunity()
	if s.templates == nil {
		return out, nil
	}
	rows, err := s.templates.ListCustom(workspaceID)
	if err != nil {
		return out, err
	}
	out = append(out, customEntries(rows)...)
	sortByDisplayName(out)
	return out, nil
}

// GetEntryForWorkspace returns one catalog entry visible to the workspace. When
// the same slug appears in several sources, the most specific wins:
// custom (workspace) > official > community.
func (s *Service) GetEntryForWorkspace(workspaceID uint, slug string) (CatalogEntry, bool) {
	list, _ := s.ListForWorkspace(workspaceID)
	rank := map[string]int{SourceCustom: 3, SourceOfficial: 2, SourceCommunity: 1}
	var best *CatalogEntry
	for i := range list {
		if list[i].Name != slug {
			continue
		}
		if best == nil || rank[list[i].Source] > rank[best.Source] {
			best = &list[i]
		}
	}
	if best != nil {
		return *best, true
	}
	return CatalogEntry{}, false
}

// GetManifestForWorkspace resolves a manifest visible to the workspace.
func (s *Service) GetManifestForWorkspace(workspaceID uint, slug, version string) (*manifest.Manifest, bool) {
	m, _, ok := s.resolveManifest(workspaceID, slug, version)
	return m, ok
}

// resolveManifest finds a template manifest, preferring a workspace custom
// import, then the synced/embedded official+community catalog. It also reports
// the source label.
func (s *Service) resolveManifest(workspaceID uint, slug, version string) (*manifest.Manifest, string, bool) {
	if s.templates != nil {
		if t, _ := s.templates.FindCustom(workspaceID, slug, version); t != nil {
			if m, err := manifest.Parse([]byte(t.RawYAML)); err == nil {
				return m, SourceCustom, true
			}
		}
	}
	return s.resolveOfficialOrCommunity(slug, version)
}

// customEntries builds listing entries from stored custom templates, grouping by
// slug and surfacing the highest version (rows arrive ordered slug, version DESC).
func customEntries(rows []models.Template) []CatalogEntry {
	versions := map[string][]string{}
	for _, t := range rows {
		versions[t.Name] = append(versions[t.Name], t.Version)
	}
	seen := map[string]bool{}
	out := []CatalogEntry{}
	for _, t := range rows {
		if seen[t.Name] {
			continue // first row per slug is the highest version
		}
		seen[t.Name] = true
		m, err := manifest.Parse([]byte(t.RawYAML))
		if err != nil {
			continue
		}
		out = append(out, entryFromManifest(m, SourceCustom, versions[t.Name]))
	}
	return out
}

// InstallInput parameterizes an install.
type InstallInput struct {
	Name        string            `json:"name"`                   // template handle
	Version     string            `json:"version,omitempty"`      // empty = latest
	DisplayName string            `json:"display_name,omitempty"` // optional display-name override
	Inputs      map[string]string `json:"inputs,omitempty"`
	// Placements optionally pins a database dependency (by manifest db name) to
	// an existing instance ID, overriding the manifest's placement. 0 = use the
	// manifest default.
	Placements map[string]uint `json:"placements,omitempty"`
	// PlacementModes optionally overrides a database dependency's placement mode (auto | dedicated |
	// shared) when no instance is pinned. Empty uses the placement declared by the template, letting a user
	// fall back to Automatic for a template that defaults to a dedicated instance.
	PlacementModes map[string]string `json:"placement_modes,omitempty"`
}

// InstallResult reports everything an install created.
type InstallResult struct {
	Template    string                     `json:"template"`     // template handle
	DisplayName string                     `json:"display_name"` // resolved install label
	Version     string                     `json:"version"`
	Stack       *models.Stack              `json:"stack,omitempty"`
	Apps        []*models.Application      `json:"apps,omitempty"`
	Databases   []*models.DatabaseInstance `json:"databases,omitempty"`
	Volumes     []*models.Volume           `json:"volumes,omitempty"`
	Configs     []*models.Config           `json:"configs,omitempty"`
	InstallID   uint                       `json:"install_id,omitempty"`
}

// Install instantiates a template version in a workspace: it validates inputs, provisions volumes,
// resolves each database dependency by placement, renders application env against those connections,
// creates the apps (grouped into a Stack when there is more than one) and deploys them.
func (s *Service) Install(ctx context.Context, workspaceID uint, in InstallInput) (*InstallResult, error) {
	return s.install(ctx, workspaceID, in, nil)
}

// install is the shared implementation behind both the synchronous Install and
// the asynchronous, progress-streaming StartInstall. report records phase
// transitions for the live UI and is nil on the synchronous path.
func (s *Service) install(ctx context.Context, workspaceID uint, in InstallInput, report *reporter) (*InstallResult, error) {
	m, source, ok := s.resolveManifest(workspaceID, in.Name, in.Version)
	if !ok {
		return nil, ErrTemplateNotFound
	}

	inputs, kept, err := resolveInputs(m, in.Inputs)
	if err != nil {
		return nil, err
	}

	result := &InstallResult{Template: m.Metadata.Name, DisplayName: displayName(m, in.DisplayName), Version: m.Metadata.Version}

	// 1. Volumes. They carry the same marketplace provenance as the install's apps
	//    and databases so the detail page attributes them to the template.
	if len(m.Volumes) > 0 {
		report.phase(PhaseVolumes, PhaseActive)
	}
	volIDs := map[string]uint{}
	for _, v := range m.Volumes {
		volMeta := models.SetBuiltin(models.Metadata{},
			models.MetaManagedBy, models.ManagedByMarketplace,
			models.MetaTemplate, m.Metadata.Name,
			models.MetaTemplateVersion, m.Metadata.Version)

		vol, err := s.volumes.Create(ctx, workspaceID, 0, result.DisplayName+" "+v.Name, 0, volMeta, nil)
		if err != nil {
			return nil, fmt.Errorf("create volume %q: %w", v.Name, err)
		}
		volIDs[v.Name] = vol.ID
		result.Volumes = append(result.Volumes, vol)
	}
	if len(m.Volumes) > 0 {
		report.phase(PhaseVolumes, PhaseDone)
	}

	// Pick the single node every resource in this install lands on: apps reach their databases over a shared Docker
	// network, which cannot span nodes, so a freshly provisioned database and the apps must be colocated. A
	// dependency binding to an existing instance makes that instance's node win; otherwise the local node.
	targetNode := s.installNode(workspaceID, m, in)

	// 2. Databases (placement-aware), building the render views. The database is named after the install —
	//    the user-chosen name, defaulting to the template name — and a multi-database template
	//    disambiguates with the dependency name.
	if len(m.Databases) > 0 {
		report.phase(PhaseDatabases, PhaseActive)
	}
	dbViews := map[string]manifest.ConnView{}
	// Logical databases created for the install, keyed by dependency name, so each
	// can be linked to the application that consumes it once the apps exist.
	dbModels := map[string]*models.Database{}
	for _, d := range m.Databases {
		report.note("Provisioning " + d.Engine + " database (" + d.Name + ")…")
		base := result.DisplayName
		if len(m.Databases) > 1 {
			base = result.DisplayName + " " + d.Name
		}
		// A user-chosen placement mode overrides the template's default (ignored
		// when an instance is pinned; d is a copy, so the manifest is untouched).
		if mode := manifest.Placement(strings.TrimSpace(in.PlacementModes[d.Name])); mode.Valid() {
			d.Placement = mode
		}
		// Provenance for a freshly provisioned instance (fresh per dependency to
		// avoid aliasing the same map across instances).
		dbMeta := models.SetBuiltin(models.Metadata{},
			models.MetaManagedBy, models.ManagedByMarketplace,
			models.MetaTemplate, m.Metadata.Name,
			models.MetaTemplateVersion, m.Metadata.Version)
		inst, logicalDB, conn, newInstance, err := s.resolveDatabase(ctx, workspaceID, base, d, in.Placements[d.Name], targetNode, dbMeta)
		if err != nil {
			return nil, fmt.Errorf("database %q: %w", d.Name, err)
		}
		dbViews[d.Name] = connView(conn)
		if logicalDB != nil {
			dbModels[d.Name] = logicalDB
		}
		if newInstance {
			result.Databases = append(result.Databases, inst)
		}
	}
	if len(m.Databases) > 0 {
		// On the async (progress-streamed) path, hold the provisioning phase open until the freshly provisioned
		// instances are online, so dependent apps deploy against a ready database. The synchronous path keeps its
		// existing non-blocking behavior (report is nil).
		if report != nil && len(result.Databases) > 0 {
			s.waitForDatabases(ctx, workspaceID, result.Databases, report)
		}
		report.phase(PhaseDatabases, PhaseDone)
	}

	// Database-only template: nothing else to do.
	if m.IsDatabaseOnly() {
		result.InstallID = s.record(workspaceID, source, m, result, kept, nil)
		return result, nil
	}

	// 3. Stack: created when grouping more than one application, or whenever the template declares a stack
	//    block, which also carries its description, annotations and shared env. Shared env is rendered and
	//    attached in step 5, once the render context exists.
	var stackID *uint
	if m.WantsStack() && s.stacks != nil {
		in := stack.Input{
			Name: result.DisplayName,
			Metadata: models.SetBuiltin(models.Metadata{},
				models.MetaManagedBy, models.ManagedByMarketplace,
				models.MetaTemplate, m.Metadata.Name,
				models.MetaTemplateVersion, m.Metadata.Version),
		}
		if m.Stack != nil {
			in.Description = m.Stack.Description
			in.Annotations = models.Metadata(m.Stack.Annotations)
		}
		st, err := s.stacks.Create(ctx, workspaceID, in)
		if err != nil {
			return nil, fmt.Errorf("create stack: %w", err)
		}
		stackID = &st.ID
		result.Stack = st
	}

	// 4. Create all apps first so every application's network alias is known
	//    before env is rendered (apps can reference each other by alias).
	report.phase(PhaseApps, PhaseActive)
	created := make([]*models.Application, 0, len(m.Applications))
	appViews := map[string]manifest.AppView{}
	for _, spec := range m.Applications {
		app, err := s.createApp(workspaceID, m, spec, result.DisplayName, stackID, targetNode)
		if err != nil {
			return nil, fmt.Errorf("create application %q: %w", spec.Name, err)
		}
		created = append(created, app)
		appViews[spec.Name] = manifest.AppView{Alias: appAlias(app)}
	}
	result.Apps = created
	report.phase(PhaseApps, PhaseDone)

	bundleKind, bundleID, bundleName := "", uint(0), ""
	switch {
	case result.Stack != nil:
		bundleKind, bundleID, bundleName = models.OwnerStack, result.Stack.ID, result.Stack.Name
	case len(created) == 1:
		bundleKind, bundleID, bundleName = models.OwnerApp, created[0].ID, created[0].Name
	}
	mounters := map[string]map[int]struct{}{}
	for i, spec := range m.Applications {
		for _, mt := range spec.Mounts {
			if mounters[mt.Volume] == nil {
				mounters[mt.Volume] = map[int]struct{}{}
			}
			mounters[mt.Volume][i] = struct{}{}
		}
	}
	for _, v := range m.Volumes {
		id := volIDs[v.Name]
		if id == 0 {
			continue
		}
		kind, ownerID, name := bundleKind, bundleID, bundleName
		if ms := mounters[v.Name]; len(ms) == 1 {
			for idx := range ms {
				kind, ownerID, name = models.OwnerApp, created[idx].ID, created[idx].Name
			}
		}
		if kind == "" {
			continue
		}
		_ = s.volumes.SetOwner(workspaceID, id, kind, ownerID, name)
	}
	if bundleKind != "" {
		for _, inst := range result.Databases {
			_ = s.dbs.SetOwner(workspaceID, inst.ID, bundleKind, bundleID, bundleName)
		}
	}

	// Link each created logical database to the application that consumes it, so it appears under the app's
	// Databases and its scoped connection is revealable there — the same link a user would make manually
	// post-install. Best-effort; the install already succeeded.
	for depName, db := range dbModels {
		if app := consumerApp(m, created, depName); app != nil {
			_, _ = s.dbs.AttachToApp(workspaceID, db.ID, app.ID, "")
		}
	}

	// 5. Render env + secrets and attach mounts for every app.
	report.phase(PhaseConfig, PhaseActive)
	r := manifest.NewRenderer(manifest.Context{Inputs: inputs, Databases: dbViews, Applications: appViews})

	// Shared stack env: declared once on the stack and injected into every member
	// app's containers at deploy time (an app-level var with the same key wins).
	if result.Stack != nil && m.Stack != nil && len(m.Stack.Env) > 0 {
		stackEnv, err := r.RenderEnv("stack", m.Stack.Env)
		if err != nil {
			return nil, fmt.Errorf("render stack env: %w", err)
		}
		secret := secretSet(m.Stack.SecretEnv)
		for k, v := range stackEnv {
			if err := s.stacks.SetEnvVar(workspaceID, result.Stack.ID, k, v, secret[k]); err != nil {
				return nil, fmt.Errorf("set stack env %s: %w", k, err)
			}
		}
	}

	// Configs are created here, after the app records exist, because a config file
	// may reference both {{ .databases.* }} and {{ .applications.*.alias }}. They
	// still land before any deploy, so the first container start sees its files.
	cfgIDs := map[string]uint{}
	if len(m.Configs) > 0 && s.configs != nil {
		report.phase(PhaseConfigs, PhaseActive)
		for _, c := range m.Configs {
			files, err := r.RenderFiles(c.Name, c.Files, c.Delimiters)
			if err != nil {
				return nil, fmt.Errorf("render config %q: %w", c.Name, err)
			}
			name, err := s.configName(workspaceID, result.DisplayName, c.Name)
			if err != nil {
				return nil, fmt.Errorf("name config %q: %w", c.Name, err)
			}
			cfg, err := s.configs.UpsertOwned(workspaceID, models.OwnerTemplateInstall, 0, configsvc.Input{
				Name:        name,
				DisplayName: result.DisplayName + " " + c.Name,
				Data:        files,
				Mode:        c.Mode,
				Sensitive:   c.Sensitive,
				Delimiters:  c.Delimiters,
			})
			if err != nil {
				return nil, fmt.Errorf("create config %q: %w", c.Name, err)
			}
			cfgIDs[c.Name] = cfg.ID
			result.Configs = append(result.Configs, cfg)
		}
		report.phase(PhaseConfigs, PhaseDone)
	}

	for i, spec := range m.Applications {
		app := created[i]
		env, err := r.RenderEnv(spec.Name, spec.Env)
		if err != nil {
			return nil, fmt.Errorf("render env for %q: %w", spec.Name, err)
		}
		secret := secretSet(spec.SecretEnv)
		for k, v := range env {
			if err := s.apps.SetEnvVar(app.ID, k, v, secret[k]); err != nil {
				return nil, fmt.Errorf("set env %s on %q: %w", k, spec.Name, err)
			}
		}
		for _, mt := range spec.Mounts {
			if mt.Config != "" {
				id := cfgIDs[mt.Config]
				if id == 0 {
					return nil, fmt.Errorf("mount config %s on %q: config was not created", mt.Config, spec.Name)
				}
				if err := s.apps.AttachConfig(app, id, mt.Key, mt.Path, mt.Mode); err != nil {
					return nil, fmt.Errorf("mount config %s on %q: %w", mt.Config, spec.Name, err)
				}
				continue
			}
			if err := s.apps.AttachVolume(app, volIDs[mt.Volume], mt.Path); err != nil {
				return nil, fmt.Errorf("mount %s on %q: %w", mt.Volume, spec.Name, err)
			}
		}
	}
	report.phase(PhaseConfig, PhaseDone)

	// 6. Deploy each configured app. The deploy phase stays active until the
	//    containers come online (StartInstall waits and completes it).
	report.phase(PhaseDeploy, PhaseActive)
	for i, spec := range m.Applications {
		app := created[i]
		report.note("Deploying " + spec.Name + "…")
		full, err := s.apps.Get(workspaceID, app.ID)
		if err != nil {
			return nil, err
		}
		if _, err := s.apps.Deploy(full, nil, "", ""); err != nil {
			return nil, fmt.Errorf("deploy %q: %w", spec.Name, err)
		}
	}

	result.InstallID = s.record(workspaceID, source, m, result, kept, cfgIDs)
	// Back-link each app to the install (best-effort; the install already
	// succeeded). Powers "installed from <template>" on the app page.
	if result.InstallID != 0 {
		id := result.InstallID
		// Trust boundary: only an official-source install may exempt its apps from
		// the restricted security profile (see Plan.AllowOfficialImageUser). Set
		// server-side here so user input can never claim official status.
		official := source == SourceOfficial
		for _, app := range created {
			app.TemplateInstallID = &id
			app.OfficialTemplate = official
			_ = s.apps.Update(app)
		}
	}
	return result, nil
}

// ListInstalls returns the workspace's template installs, annotated with whether
// a newer version of each template is available.
func (s *Service) ListInstalls(workspaceID uint) ([]InstallView, error) {
	if s.installs == nil {
		return nil, nil
	}
	rows, err := s.installs.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]InstallView, 0, len(rows))
	for i := range rows {
		latest := s.latestVersion(workspaceID, rows[i].TemplateName)
		view := InstallView{
			TemplateInstall: rows[i],
			LatestVersion:   latest,
			UpdateAvailable: isNewer(latest, rows[i].Version),
		}
		// Resolve the apps the install created, for linking (deleted apps drop off).
		for _, id := range rows[i].AppIDs {
			if app, err := s.apps.Get(workspaceID, id); err == nil {
				view.Apps = append(view.Apps, InstallAppRef{ID: app.ID, Name: app.Name, DisplayName: app.DisplayName, Status: string(app.Status)})
			}
		}
		out = append(out, view)
	}
	return out, nil
}

func resourceName(installName, localName string, exists func(string) (bool, error)) (string, error) {
	return slug.Unique(installName+"-"+localName, localName, exists)
}

func (s *Service) configName(workspaceID uint, installName, localName string) (string, error) {
	return resourceName(installName, localName, func(candidate string) (bool, error) {
		return s.configs.ExistsByName(workspaceID, candidate), nil
	})
}

func (s *Service) createApp(workspaceID uint, m *manifest.Manifest, spec manifest.AppSpec, baseName string, stackID *uint, serverID uint) (*models.Application, error) {

	name := baseName
	if len(m.Applications) > 1 {
		name = fmt.Sprintf("%s %s", baseName, spec.Name)
	}
	in := application.CreateInput{
		DisplayName: name,
		SourceType:  models.AppSourceImage,
		Icon:        m.Metadata.Icon,
		Image:       spec.Image,
		Tag:         spec.Tag,
		Command:     spec.Command,
		StackID:     stackID,
		ServerID:    serverID,
		Metadata: models.SetBuiltin(models.Metadata{},
			models.MetaManagedBy, models.ManagedByMarketplace,
			models.MetaTemplate, m.Metadata.Name,
			models.MetaTemplateVersion, m.Metadata.Version,
		),
	}
	for _, p := range spec.Ports {
		in.Ports = append(in.Ports, application.PortSpec{ContainerPort: p.Container, Protocol: "tcp", Scheme: p.Scheme})
	}
	if len(spec.Ports) > 0 {
		in.Port = spec.Ports[0].Container
	}
	if spec.Resources != nil {
		in.MemoryBytes, _ = spec.Resources.MemoryBytes()
		in.NanoCPUs, _ = spec.Resources.NanoCPUs()
	}
	return s.apps.Create(workspaceID, in)
}

// resolveDatabase satisfies a database dependency per its placement, delegating to the shared placement
// resolver so marketplace installs and GitOps apply behave identically. declName is empty here: an install
// materializes the app's env immediately rather than via a reconcile, so its databases carry no declarative name.
func (s *Service) resolveDatabase(ctx context.Context, workspaceID uint, base string, d manifest.Database, pinInstance, serverID uint, meta models.Metadata) (*models.DatabaseInstance, *models.Database, database.ConnectionInfo, bool, error) {
	return s.dbs.ResolveDependency(ctx, workspaceID, serverID, pinInstance, base, "",
		models.DBEngine(d.Engine), d.Version, database.Placement(d.Placement), meta)
}

// consumerApp returns the created application consuming the database dependency depName — the app referencing
// it in env, preferring one marked primary. A logical database is owned by at most one app, so the primary
// (else the first) wins. Falls back to the sole app; nil when the consumer is ambiguous.
func consumerApp(m *manifest.Manifest, created []*models.Application, depName string) *models.Application {
	token := ".databases." + depName + "."
	var match *models.Application
	for i, spec := range m.Applications {
		if i >= len(created) {
			break
		}
		if !referencesDep(spec.Env, token) {
			continue
		}
		if spec.Primary {
			return created[i]
		}
		if match == nil {
			match = created[i]
		}
	}
	// Connection details declared once on the stack are shared by every member, so
	// no single app's env names the dependency. The primary owns the link then —
	// otherwise moving them onto the stack would silently unlink the database.
	if match == nil && m.Stack != nil && referencesDep(m.Stack.Env, token) {
		for i, spec := range m.Applications {
			if i < len(created) && spec.Primary {
				return created[i]
			}
		}
		if len(created) > 0 {
			return created[0]
		}
	}
	if match == nil && len(created) == 1 {
		return created[0]
	}
	return match
}

// referencesDep reports whether any value interpolates the dependency token.
func referencesDep(env map[string]string, token string) bool {
	for _, v := range env {
		if strings.Contains(v, token) {
			return true
		}
	}
	return false
}

// installNode decides the single node every resource in an install must land on, so each app can share a Docker
// network with the databases it uses — a network cannot span nodes. A dependency binding to an existing
// instance makes that instance's node authoritative; the first such binding wins. Otherwise 0, the local node.
func (s *Service) installNode(workspaceID uint, m *manifest.Manifest, in InstallInput) uint {
	for _, d := range m.Databases {
		// A pinned instance dictates the node outright.
		if id := in.Placements[d.Name]; id != 0 {
			if inst, err := s.dbs.Get(workspaceID, id); err == nil {
				return inst.ServerID
			}
			continue
		}
		placement := d.Placement
		if mode := manifest.Placement(strings.TrimSpace(in.PlacementModes[d.Name])); mode.Valid() {
			placement = mode
		}
		engine := models.DBEngine(d.Engine)
		// Shared always reuses an existing instance; auto reuses one when the engine
		// has logical databases and a running instance exists. Dedicated never does.
		reuses := placement == manifest.PlacementShared ||
			(placement == manifest.PlacementAuto && models.EngineSupportsLogicalDatabases(engine))
		if reuses {
			if inst := s.dbs.FindReusableInstance(workspaceID, engine); inst != nil {
				return inst.ServerID
			}
		}
	}
	return 0
}

// record persists provenance (best-effort; a failure here never fails an install
// that already succeeded).
func (s *Service) record(workspaceID uint, source string, m *manifest.Manifest, r *InstallResult, inputs map[string]string, cfgIDs map[string]uint) uint {
	if s.installs == nil {
		return 0
	}
	rec := &models.TemplateInstall{
		WorkspaceID: workspaceID, Source: source,
		TemplateName: m.Metadata.Name, TemplateDisplayName: m.Metadata.DisplayName, Version: m.Metadata.Version,
		DisplayName: r.DisplayName, StackID: stackIDOf(r), Inputs: inputs,
	}
	if len(cfgIDs) > 0 {
		rec.ConfigIDs = cfgIDs
	}
	for _, a := range r.Apps {
		rec.AppIDs = append(rec.AppIDs, a.ID)
	}
	for _, d := range r.Databases {
		rec.DatabaseIDs = append(rec.DatabaseIDs, d.ID)
	}
	for _, v := range r.Volumes {
		rec.VolumeIDs = append(rec.VolumeIDs, v.ID)
	}
	if err := s.installs.Create(rec); err != nil {
		return 0
	}
	return rec.ID
}

func stackIDOf(r *InstallResult) *uint {
	if r.Stack != nil {
		return &r.Stack.ID
	}
	return nil
}

// resolveInputs validates and fills install inputs. It returns the full input
// map for rendering and a "kept" subset (non-secret) for provenance.
func resolveInputs(m *manifest.Manifest, given map[string]string) (all, kept map[string]string, err error) {
	all = map[string]string{}
	kept = map[string]string{}
	for _, in := range m.Inputs {
		v := strings.TrimSpace(given[in.Key])
		if v == "" {
			v = in.Default
		}
		if v == "" && in.Generate {
			n := in.Length
			if n <= 0 {
				n = 24
			}
			v = manifest.RandAlphaNum(n)
		}
		if v == "" && in.Required {
			return nil, nil, fmt.Errorf("%w: %s", ErrMissingInput, in.Key)
		}
		if v != "" && in.Pattern != "" {
			ok, _ := regexp.MatchString(in.Pattern, v)
			if !ok {
				return nil, nil, fmt.Errorf("%w: %s", ErrInvalidInput, in.Key)
			}
		}
		all[in.Key] = v
		if in.Type != manifest.InputPassword && !in.Generate {
			kept[in.Key] = v
		}
	}
	return all, kept, nil
}

func connView(c database.ConnectionInfo) manifest.ConnView {
	return manifest.ConnView{
		Host: c.Host, Port: strconv.Itoa(c.Port), User: c.Username,
		Password: c.Password, Name: c.Database, URI: c.URI,
	}
}

func secretSet(keys []string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func displayName(m *manifest.Manifest, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if dn := strings.TrimSpace(m.Metadata.DisplayName); dn != "" {
		return dn
	}
	return m.Metadata.Name
}

func appAlias(app *models.Application) string {
	if app.Alias != "" {
		return app.Alias
	}
	return fmt.Sprintf("mb-app-%d", app.ID)
}

var nonName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeName(s string) string {
	return strings.Trim(nonName.ReplaceAllString(s, "-"), "-")
}
