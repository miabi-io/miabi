// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package marketplace

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

type fakeStore struct {
	srcID uint
	rows  []models.Template
}

func (f *fakeStore) EnsureCustomSource(workspaceID uint) (*models.TemplateSource, error) {
	if f.srcID == 0 {
		f.srcID = 1
	}
	return &models.TemplateSource{ID: f.srcID, Type: models.TemplateSourceCustom, WorkspaceID: &workspaceID}, nil
}

func (f *fakeStore) UpsertTemplate(t *models.Template) error {
	for i := range f.rows {
		if f.rows[i].Name == t.Name && f.rows[i].Version == t.Version {
			f.rows[i] = *t
			return nil
		}
	}
	f.rows = append(f.rows, *t)
	return nil
}

func (f *fakeStore) ListCustom(workspaceID uint) ([]models.Template, error) { return f.rows, nil }

func (f *fakeStore) DeleteCustom(workspaceID uint, slug string) (int64, error) {
	var kept []models.Template
	var n int64
	for _, t := range f.rows {
		if t.Name == slug {
			n++
			continue
		}
		kept = append(kept, t)
	}
	f.rows = kept
	return n, nil
}

func (f *fakeStore) FindCustom(workspaceID uint, slug, version string) (*models.Template, error) {
	for i := range f.rows {
		if f.rows[i].Name == slug && (version == "" || f.rows[i].Version == version) {
			return &f.rows[i], nil
		}
	}
	return nil, nil
}

const customYAML = `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: my-app, displayName: My App, version: "1.0.0", category: Custom}
applications:
  - name: app
    image: nginx
    ports:
      - container: 8080
`

func TestImportStoresAndResolves(t *testing.T) {
	svc := &Service{templates: &fakeStore{}}
	entry, err := svc.Import(7, customYAML)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if entry.Name != "my-app" || entry.Source != SourceCustom {
		t.Errorf("entry = %+v", entry)
	}

	// resolveManifest prefers the custom import...
	if m, src, ok := svc.resolveManifest(7, "my-app", ""); !ok || src != SourceCustom || m.Metadata.DisplayName != "My App" {
		t.Errorf("resolve custom: ok=%v src=%q", ok, src)
	}
	// ...and still falls back to the embedded official catalog.
	if _, src, ok := svc.resolveManifest(7, "postgresql", ""); !ok || src != SourceOfficial {
		t.Errorf("resolve official fallback: ok=%v src=%q", ok, src)
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"1.2.0", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"", "1.0.0", false}, // template no longer in catalog
		{" 2.0.0 ", "1.0.0", true},
	}
	for _, c := range cases {
		if got := isNewer(c.latest, c.current); got != c.want {
			t.Errorf("isNewer(%q,%q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestUpdateCustom(t *testing.T) {
	svc := &Service{templates: &fakeStore{}}
	if _, err := svc.Import(7, customYAML); err != nil {
		t.Fatal(err)
	}

	// GetCustomYAML returns the stored manifest for editing.
	raw, ok := svc.GetCustomYAML(7, "my-app", "")
	if !ok || raw != customYAML {
		t.Fatalf("GetCustomYAML ok=%v len=%d", ok, len(raw))
	}

	// A valid edit replaces the manifest in place.
	updated := strings.Replace(customYAML, "displayName: My App", "displayName: Renamed App", 1)
	entry, err := svc.UpdateCustom(7, "my-app", updated)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if entry.DisplayName != "Renamed App" {
		t.Errorf("entry.DisplayName = %q, want Renamed App", entry.DisplayName)
	}

	// Editing a template that does not exist is a not-found.
	if _, err := svc.UpdateCustom(7, "postgresql", customYAML); !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("update missing: err = %v, want ErrTemplateNotFound", err)
	}

	// The handle (name) may not be changed by an edit.
	reslugged := strings.Replace(customYAML, "name: my-app", "name: other", 1)
	if _, err := svc.UpdateCustom(7, "my-app", reslugged); !errors.Is(err, ErrInvalidTemplate) {
		t.Errorf("rename handle: err = %v, want ErrInvalidTemplate", err)
	}
}

func TestDeleteCustom(t *testing.T) {
	svc := &Service{templates: &fakeStore{}}
	if _, err := svc.Import(7, customYAML); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCustom(7, "my-app"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := svc.GetEntryForWorkspace(7, "my-app"); ok {
		t.Error("template should be gone after delete")
	}
	if err := svc.DeleteCustom(7, "my-app"); !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("delete again: err = %v, want ErrTemplateNotFound", err)
	}
}

func TestImportRejectsInvalid(t *testing.T) {
	svc := &Service{templates: &fakeStore{}}
	if _, err := svc.Import(7, "not: a: valid: template"); err == nil {
		t.Fatal("expected invalid-template error")
	}
}

func TestListForWorkspaceMergesCustom(t *testing.T) {
	svc := &Service{templates: &fakeStore{}}
	if _, err := svc.Import(7, customYAML); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListForWorkspace(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(List())+1 {
		t.Errorf("merged list = %d, want builtin+1 = %d", len(list), len(List())+1)
	}
	e, ok := svc.GetEntryForWorkspace(7, "my-app")
	if !ok || e.Source != SourceCustom {
		t.Errorf("custom entry not found via workspace lookup")
	}
}

// TestCatalogLoads asserts the embedded official catalog parses, its digests
// match, and every template validates — the CI guard the README promises.
func TestCatalogLoads(t *testing.T) {
	if err := LoadError(); err != nil {
		t.Fatalf("embedded catalog failed to load: %v", err)
	}
	if len(List()) == 0 {
		t.Fatal("catalog should not be empty")
	}
}

func TestCatalogLookup(t *testing.T) {
	if _, ok := GetEntry("postgresql"); !ok {
		t.Error("postgresql template should exist")
	}
	if _, ok := GetEntry("nonexistent"); ok {
		t.Error("nonexistent template should not be found")
	}
	if m, ok := GetManifest("minio", ""); !ok || m.Metadata.Name != "minio" {
		t.Error("latest minio manifest should resolve")
	}
}

// TestCatalogShapes spot-checks the three template shapes the catalog must cover.
func TestCatalogShapes(t *testing.T) {
	// App with a persistent volume: MinIO provisions object storage backed by a volume.
	minio, _ := GetManifest("minio", "")
	if len(minio.Volumes) != 1 || len(minio.Applications) != 1 || len(minio.Databases) != 0 {
		t.Errorf("minio shape: dbs=%d vols=%d svcs=%d", len(minio.Databases), len(minio.Volumes), len(minio.Applications))
	}
	// Database-only: postgresql has no services.
	pg, _ := GetManifest("postgresql", "")
	if !pg.IsDatabaseOnly() {
		t.Error("postgresql should be database-only")
	}
	// Plain single service: nginx has no deps.
	nginx, _ := GetManifest("nginx", "")
	if len(nginx.Databases) != 0 || len(nginx.Applications) != 1 {
		t.Error("nginx should be a single dependency-free service")
	}
}

func TestResolveInputsGeneratesAndValidates(t *testing.T) {
	// MinIO auto-generates its root password (type=password, generate=true).
	minio, _ := GetManifest("minio", "")
	all, kept, err := resolveInputs(minio, nil)
	if err != nil {
		t.Fatalf("resolveInputs: %v", err)
	}
	if all["root_password"] == "" {
		t.Error("root_password should have been auto-generated")
	}
	if _, ok := kept["root_password"]; ok {
		t.Error("generated/password inputs must not be kept in provenance")
	}
}

// lengthFixture / requiredFixture are inline manifests: the embedded floor is data/infra primitives, so
// the input shapes these tests exercise (explicit length, default length, required + pattern) are modeled
// here rather than borrowed from a shipped template.
const lengthFixture = `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: fixture, displayName: Fixture, version: "1.0.0", category: Custom}
inputs:
  - {key: encryption_key, type: password, generate: true, length: 32, required: true}
  - {key: jwt_secret, type: password, generate: true, required: true}
applications:
  - name: app
    image: nginx
    ports:
      - container: 8080
`

const requiredFixture = `
apiVersion: miabi.io/v1
kind: Template
metadata: {name: fixture, displayName: Fixture, version: "1.0.0", category: Custom}
inputs:
  - {key: site_url, type: string, required: true, pattern: "^https?://"}
applications:
  - name: app
    image: nginx
    ports:
      - container: 8080
`

func TestResolveInputsHonorsLength(t *testing.T) {
	m, err := manifest.Parse([]byte(lengthFixture))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	all, _, err := resolveInputs(m, nil)
	if err != nil {
		t.Fatalf("resolveInputs: %v", err)
	}
	if len(all["encryption_key"]) != 32 {
		t.Errorf("encryption_key len = %d, want 32 (AES-256)", len(all["encryption_key"]))
	}
	if len(all["jwt_secret"]) != 24 {
		t.Errorf("jwt_secret len = %d, want 24 (default)", len(all["jwt_secret"]))
	}
}

func TestResolveInputsRequired(t *testing.T) {
	m, err := manifest.Parse([]byte(requiredFixture))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if _, _, err := resolveInputs(m, nil); err == nil {
		t.Fatal("expected missing-input error for required site_url")
	}
	if _, _, err := resolveInputs(m, map[string]string{"site_url": "not-a-url"}); err == nil {
		t.Fatal("expected invalid-input error for pattern mismatch")
	}
}

func TestConsumerApp(t *testing.T) {
	app := func(id uint) *models.Application { return &models.Application{ID: id} }

	// Single app referencing the db → linked to it.
	single := &manifest.Manifest{Applications: []manifest.AppSpec{
		{Name: "web", Env: map[string]string{"DB_HOST": "{{ .databases.db.host }}"}},
	}}
	if got := consumerApp(single, []*models.Application{app(10)}, "db"); got == nil || got.ID != 10 {
		t.Fatalf("single: want app 10, got %v", got)
	}

	// Multiple apps; the primary that references the db wins over an earlier one.
	multi := &manifest.Manifest{Applications: []manifest.AppSpec{
		{Name: "worker", Env: map[string]string{"DB_HOST": "{{ .databases.db.host }}"}},
		{Name: "web", Primary: true, Env: map[string]string{"DB_URL": "{{ .databases.db.url }}"}},
	}}
	if got := consumerApp(multi, []*models.Application{app(1), app(2)}, "db"); got == nil || got.ID != 2 {
		t.Fatalf("multi-primary: want app 2, got %v", got)
	}

	// Name matching is token-precise: querying "d" must not match ".databases.db.".
	// Use a multi-app manifest so the sole-app fallback doesn't mask the result.
	if got := consumerApp(multi, []*models.Application{app(1), app(2)}, "d"); got != nil {
		t.Fatalf("name-precision: want nil for non-matching dep prefix, got %v", got)
	}

	// Ambiguous: two apps, neither references the dep → no link.
	none := &manifest.Manifest{Applications: []manifest.AppSpec{{Name: "a"}, {Name: "b"}}}
	if got := consumerApp(none, []*models.Application{app(1), app(2)}, "db"); got != nil {
		t.Fatalf("ambiguous: want nil, got %v", got)
	}

	// Single app that doesn't reference the dep still falls back to the sole app.
	soleNoRef := &manifest.Manifest{Applications: []manifest.AppSpec{{Name: "a"}}}
	if got := consumerApp(soleNoRef, []*models.Application{app(7)}, "db"); got == nil || got.ID != 7 {
		t.Fatalf("sole-fallback: want app 7, got %v", got)
	}
}
