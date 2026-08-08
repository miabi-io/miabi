// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	d "github.com/miabi-io/miabi/internal/declarative"
)

const projectYAML = `
apiVersion: miabi.io/v1
kind: Volume
metadata: { name: data }
spec: { size: 5Gi }
---
apiVersion: miabi.io/v1
kind: Database
metadata: { name: db }
spec: { engine: postgres, version: "16-alpine" }
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: ghcr.io/org/web
  digest: sha256:deadbeef
  ports: [{ container: 8080 }]
  env:
    DATABASE_URL: "{{ .databases.db.uri }}"
    SECRET_KEY: "{{ .secrets.app_key }}"
  secretEnv: [SECRET_KEY]
  mounts: [{ volume: data, path: /data }]
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web-public }
spec: { hosts: [web.example.com, www.web.example.com], app: web, port: 8080 }
`

func TestParseValidSet(t *testing.T) {
	set, err := d.Parse([]byte(projectYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if set.Len() != 4 {
		t.Fatalf("want 4 resources, got %d", set.Len())
	}
	app, ok := set.Get("Application/web")
	if !ok || app.Application == nil {
		t.Fatal("missing Application/web")
	}
	if app.Application.Ports[0].Scheme != "http" {
		t.Errorf("port scheme not defaulted: %q", app.Application.Ports[0].Scheme)
	}
	dom, _ := set.Get("Route/web-public")
	if dom.Route.TLS != "acme" {
		t.Errorf("tls not defaulted: %q", dom.Route.TLS)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Volume
metadata: { name: data }
spec: { sizes: 5Gi }
`
	if _, err := d.Parse([]byte(y)); err == nil {
		t.Fatal("expected error for unknown spec field")
	}
}

func TestParseRejectsDanglingReferences(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: nginx
  mounts: [{ volume: missing, path: /data }]
`
	if _, err := d.Parse([]byte(y)); err == nil {
		t.Fatal("expected error for mount referencing unknown volume")
	}
}

func TestParseRejectsBadEngine(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Database
metadata: { name: db }
spec: { engine: oracle }
`
	if _, err := d.Parse([]byte(y)); err == nil {
		t.Fatal("expected error for unsupported engine")
	}
}

func TestParseAcceptsLabelsAndAnnotations(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Application
metadata:
  name: web
  labels:
    env: prod
    region: eu2
    dc: "2"
  annotations:
    miabi.io/owner: platform-team
    miabi.io/description: "Public storefront web tier"
spec: { image: nginx }
`
	set, err := d.Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	app, _ := set.Get("Application/web")
	if app.Metadata.Labels["env"] != "prod" {
		t.Errorf("label env not parsed: %v", app.Metadata.Labels)
	}
	if app.Metadata.Annotations["miabi.io/description"] != "Public storefront web tier" {
		t.Errorf("annotation not parsed: %v", app.Metadata.Annotations)
	}
}

func TestParseRejectsBadLabelKeyAndValue(t *testing.T) {
	cases := map[string]string{
		"bad label key":      "metadata:\n  name: web\n  labels: { \"bad key!\": x }",
		"bad label value":    "metadata:\n  name: web\n  labels: { env: \"not valid!\" }",
		"bad annotation key": "metadata:\n  name: web\n  annotations: { \"@nope\": x }",
	}
	for name, meta := range cases {
		y := "apiVersion: miabi.io/v1\nkind: Application\n" + meta + "\nspec: { image: nginx }\n"
		if _, err := d.Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestParseAllowsArbitraryAnnotationValues(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Application
metadata:
  name: web
  annotations:
    note: "anything goes here: spaces, punctuation! and even / slashes."
spec: { image: nginx }
`
	if _, err := d.Parse([]byte(y)); err != nil {
		t.Fatalf("annotation values should be unconstrained, got: %v", err)
	}
}

func TestParseRejectsMisplacedMetadataKey(t *testing.T) {
	// A custom key written directly under metadata (instead of under labels)
	// must be rejected, not silently dropped.
	const y = `
apiVersion: miabi.io/v1
kind: Application
metadata:
  name: web
  region: eu2
spec: { image: nginx }
`
	if _, err := d.Parse([]byte(y)); err == nil {
		t.Fatal("expected error for unknown metadata field")
	}
}

func TestParseRejectsUnknownTopLevelKey(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
labels: { env: prod }
spec: { image: nginx }
`
	if _, err := d.Parse([]byte(y)); err == nil {
		t.Fatal("expected error for misplaced top-level key")
	}
}

func TestEdges(t *testing.T) {
	set, err := d.Parse([]byte(projectYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := map[string]d.EdgeType{}
	for _, e := range d.Edges(set) {
		got[e.From+" -> "+e.To] = e.Type
	}
	want := map[string]d.EdgeType{
		"Application/web -> Volume/data":      d.EdgeMount,
		"Application/web -> Database/db":      d.EdgeDatabase,
		"Route/web-public -> Application/web": d.EdgeRoute,
	}
	for k, typ := range want {
		if got[k] != typ {
			t.Errorf("edge %q: want type %q, got %q", k, typ, got[k])
		}
	}
	// The {{ .secrets.app_key }} reference must NOT produce an edge: no Secret
	// named app_key is declared in the set, so there is no node to link to.
	for k := range got {
		if k == "Application/web -> Secret/app_key" {
			t.Errorf("unexpected edge to undeclared secret: %q", k)
		}
	}
	if len(got) != len(want) {
		t.Errorf("edge count: want %d, got %d (%v)", len(want), len(got), got)
	}
}

func TestEdgesSecretAndDomain(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Secret
metadata: { name: appkey }
spec: { value: s3cr3t }
---
apiVersion: miabi.io/v1
kind: Domain
metadata: { name: example.com }
spec: { tls: acme }
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: nginx
  env: { SECRET_KEY: "{{ .secrets.appkey }}" }
  secretEnv: [SECRET_KEY]
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web-public }
spec: { hosts: [shop.example.com], app: web }
`
	set, err := d.Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := map[string]d.EdgeType{}
	for _, e := range d.Edges(set) {
		got[e.From+" -> "+e.To] = e.Type
	}
	if got["Application/web -> Secret/appkey"] != d.EdgeSecret {
		t.Errorf("missing secret edge: %v", got)
	}
	// shop.example.com is a subdomain of the declared Domain example.com.
	if got["Route/web-public -> Domain/example.com"] != d.EdgeDomain {
		t.Errorf("missing domain edge: %v", got)
	}
}

// GitOps relies on ParseFS distinguishing an empty manifest set (ErrNoResources,
// which AllowEmpty can turn into a teardown) from a missing path (fs.ErrNotExist,
// always an error — never a prune).
func TestParseFSDistinguishesEmptyFromMissing(t *testing.T) {
	// Path exists but holds no manifests → ErrNoResources.
	fsys := fstest.MapFS{
		"envs/prod/README.md": {Data: []byte("# not a manifest")},
	}
	if _, err := d.ParseFS(fsys, "envs/prod"); !errors.Is(err, d.ErrNoResources) {
		t.Errorf("empty path: want ErrNoResources, got %v", err)
	}
	// Path does not exist → fs.ErrNotExist (a configuration error).
	if _, err := d.ParseFS(fsys, "envs/missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing path: want fs.ErrNotExist, got %v", err)
	}
}

func TestProjectFlattensChildren(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Project
metadata: { name: shop }
spec:
  description: storefront
  resources:
    - apiVersion: miabi.io/v1
      kind: Volume
      metadata: { name: data }
      spec: {}
    - apiVersion: miabi.io/v1
      kind: Application
      metadata: { name: web }
      spec: { image: nginx }
`
	set, err := d.Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse project: %v", err)
	}
	if !set.Has(d.KindApplication, "web") || !set.Has(d.KindVolume, "data") {
		t.Fatal("project children were not flattened into the set")
	}
}

func TestBuildPlanCreateUpdateNoopDelete(t *testing.T) {
	desired, err := d.Parse([]byte(projectYAML))
	if err != nil {
		t.Fatalf("parse desired: %v", err)
	}

	// Actual: db identical, web with an older digest, plus an orphan volume.
	actual := d.NewResourceSet()
	actual.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindVolume,
		Metadata: d.Meta{Name: "data"}, Volume: &d.VolumeSpec{Size: "5Gi"},
	})
	actual.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindDatabase,
		Metadata: d.Meta{Name: "db"}, Database: &d.DatabaseSpec{Engine: "postgres", Version: "16-alpine", Placement: "auto"},
	})
	actual.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindApplication,
		Metadata: d.Meta{Name: "web"},
		Application: &d.ApplicationSpec{
			Image: "ghcr.io/org/web", Digest: "sha256:oldoldold",
			Ports:     []d.PortSpec{{Container: 8080, Scheme: "http"}},
			Env:       map[string]string{"DATABASE_URL": "{{ .databases.db.uri }}", "SECRET_KEY": "{{ .secrets.app_key }}"},
			SecretEnv: []string{"SECRET_KEY"},
			Mounts:    []d.MountSpec{{Volume: "data", Path: "/data"}},
		},
	})
	actual.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindVolume,
		Metadata: d.Meta{Name: "orphan"}, Volume: &d.VolumeSpec{},
	})

	plan := d.BuildPlan(desired, actual, d.PlanOptions{Prune: true})
	c, u, del, _ := plan.Counts()
	if c != 1 { // Route/web-public is new
		t.Errorf("creates: want 1, got %d (%+v)", c, plan.Changes)
	}
	if u != 1 { // web digest changed
		t.Errorf("updates: want 1, got %d", u)
	}
	if del != 1 { // orphan volume pruned
		t.Errorf("deletes: want 1, got %d", del)
	}

	// Ordering: the delete must precede the route create (teardown before
	// dependent setup), and the create (route, rank 3) after the update (app).
	if plan.Changes[0].Action != d.ActionDelete {
		t.Errorf("expected delete first, got %s", plan.Changes[0].Action)
	}

	// The update must carry the digest field diff.
	var sawDigest bool
	for _, ch := range plan.Changes {
		if ch.Action == d.ActionUpdate {
			for _, f := range ch.Fields {
				if f.Field == "digest" && f.To == "sha256:deadbeef" {
					sawDigest = true
				}
			}
		}
	}
	if !sawDigest {
		t.Error("update change did not record the digest diff")
	}
}

func TestDomainKind(t *testing.T) {
	src := `apiVersion: miabi.io/v1
kind: Domain
metadata: { name: shop.example.com }
spec:
  tls: acme
  wildcard: true`
	set, err := d.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse domain: %v", err)
	}
	doms := set.ByKind(d.KindDomain)
	if len(doms) != 1 {
		t.Fatalf("want 1 domain, got %d", len(doms))
	}
	if doms[0].Metadata.Name != "shop.example.com" {
		t.Errorf("FQDN name not preserved: %q", doms[0].Metadata.Name)
	}
	if doms[0].Domain == nil || doms[0].Domain.TLS != "acme" || !doms[0].Domain.Wildcard {
		t.Errorf("domain spec mismatch: %+v", doms[0].Domain)
	}

	// A TLS change is a diffable update; an off-mode is rejected for domains.
	actual := d.NewResourceSet()
	actual.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindDomain,
		Metadata: d.Meta{Name: "shop.example.com"},
		Domain:   &d.DomainSpec{TLS: "custom", Wildcard: true},
	})
	plan := d.BuildPlan(set, actual, d.PlanOptions{})
	_, u, _, _ := plan.Counts()
	if u != 1 {
		t.Errorf("tls change should be 1 update, got %d (%+v)", u, plan.Changes)
	}

	if _, err := d.Parse([]byte("apiVersion: miabi.io/v1\nkind: Domain\nmetadata: { name: shop.example.com }\nspec: { tls: off }")); err == nil {
		t.Error("tls: off must be rejected for a Domain")
	}
	if _, err := d.Parse([]byte("apiVersion: miabi.io/v1\nkind: Domain\nmetadata: { name: NotAHost_name }")); err == nil {
		t.Error("invalid hostname metadata.name must be rejected")
	}
}

func TestApplicationPortExposure(t *testing.T) {
	src := `apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: ghcr.io/org/web
  externalLabel: web
  ports:
    - container: 8080
      externalAccess: true
    - container: 5432
      protocol: tcp
      hostPort: 15432`
	set, err := d.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	app := set.ByKind(d.KindApplication)[0].Application
	if app.ExternalLabel != "web" {
		t.Errorf("externalLabel not parsed: %q", app.ExternalLabel)
	}
	// hostPort implies publish (set during normalize/validate).
	var p5432 d.PortSpec
	for _, p := range app.Ports {
		if p.Container == 5432 {
			p5432 = p
		}
	}
	if !p5432.Publish || p5432.HostPort != 15432 {
		t.Errorf("hostPort should imply publish: %+v", p5432)
	}

	// Diff is presence-based: an app with the same image but no exposure differs
	// (the externalAccess/published port flags), producing one update.
	actual := d.NewResourceSet()
	actual.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindApplication, Metadata: d.Meta{Name: "web"},
		Application: &d.ApplicationSpec{
			Image: "ghcr.io/org/web",
			Ports: []d.PortSpec{{Container: 8080, Scheme: "http"}, {Container: 5432, Protocol: "tcp"}},
		},
	})
	plan := d.BuildPlan(set, actual, d.PlanOptions{})
	_, u, _, _ := plan.Counts()
	if u != 1 {
		t.Errorf("exposure change should be 1 update, got %d (%+v)", u, plan.Changes)
	}
	// Converged state (both flags present) is a no-op.
	actual2 := d.NewResourceSet()
	actual2.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindApplication, Metadata: d.Meta{Name: "web"},
		Application: &d.ApplicationSpec{
			Image:         "ghcr.io/org/web",
			ExternalLabel: "web",
			Ports:         []d.PortSpec{{Container: 8080, Scheme: "http", ExternalAccess: true}, {Container: 5432, Protocol: "tcp", Publish: true, HostPort: 15432}},
		},
	})
	if _, u2, _, _ := d.BuildPlan(set, actual2, d.PlanOptions{}).Counts(); u2 != 0 {
		t.Errorf("converged exposure should be noop, got %d updates", u2)
	}

	// Validation rejects a bad protocol and an out-of-range host port.
	if _, err := d.Parse([]byte("apiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: web }\nspec: { image: x, ports: [{ container: 80, protocol: sctp }] }")); err == nil {
		t.Error("invalid protocol must be rejected")
	}
}

func TestBuildPlanPruneManagedByOnlyDeletesOwned(t *testing.T) {
	desired := d.NewResourceSet()
	desired.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindVolume, Metadata: d.Meta{Name: "keep"}, Volume: &d.VolumeSpec{}})

	actual := d.NewResourceSet()
	actual.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindVolume, Metadata: d.Meta{Name: "keep"}, Volume: &d.VolumeSpec{}})
	// Owned by GitOps -> prunable.
	actual.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindVolume,
		Metadata: d.Meta{Name: "gitops-orphan", Labels: map[string]string{d.LabelManagedBy: "gitops"}}, Volume: &d.VolumeSpec{}})
	// Hand-created -> must never be pruned.
	actual.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindVolume,
		Metadata: d.Meta{Name: "user-orphan", Labels: map[string]string{d.LabelManagedBy: "user"}}, Volume: &d.VolumeSpec{}})
	// Unlabeled -> must never be pruned.
	actual.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindVolume, Metadata: d.Meta{Name: "bare-orphan"}, Volume: &d.VolumeSpec{}})

	plan := d.BuildPlan(desired, actual, d.PlanOptions{Prune: true, PruneManagedBy: "gitops"})
	var deletes []string
	for _, c := range plan.Changes {
		if c.Action == d.ActionDelete {
			deletes = append(deletes, c.Name)
		}
	}
	if len(deletes) != 1 || deletes[0] != "gitops-orphan" {
		t.Fatalf("managed prune deleted the wrong set: %v (want only gitops-orphan)", deletes)
	}
}

// TestBuildPlanPruneGitOpsSourceScopesToOwnProject models two GitOps projects backed by one repo
// at different subpaths in the same workspace snapshot. Without source scoping each project saw
// the other's app as an orphan and tore it down, so the two apps endlessly replaced each other.
func TestBuildPlanPruneGitOpsSourceScopesToOwnProject(t *testing.T) {
	app := func(name, source string) d.Resource {
		labels := map[string]string{d.LabelManagedBy: "gitops"}
		if source != "" {
			labels[d.LabelGitOpsSource] = source
		}
		return d.Resource{APIVersion: d.APIVersion, Kind: d.KindApplication,
			Metadata: d.Meta{Name: name, Labels: labels}, Application: &d.ApplicationSpec{Image: "img"}}
	}

	// Workspace snapshot holds both projects' apps.
	actual := d.NewResourceSet()
	actual.Add(app("okapi", "1"))
	actual.Add(app("posta", "2"))

	// Reconcile of project "okapi" (source id "1"): desired is only its own app.
	desired := d.NewResourceSet()
	desired.Add(app("okapi", "1"))

	plan := d.BuildPlan(desired, actual, d.PlanOptions{Prune: true, PruneManagedBy: "gitops", PruneGitOpsSource: "1"})
	for _, c := range plan.Changes {
		if c.Action == d.ActionDelete {
			t.Fatalf("scoped prune deleted %s/%s; must not touch another project's resource", c.Kind, c.Name)
		}
	}

	// A genuine orphan owned by this same source is still pruned.
	desiredEmpty := d.NewResourceSet()
	plan = d.BuildPlan(desiredEmpty, actual, d.PlanOptions{Prune: true, PruneManagedBy: "gitops", PruneGitOpsSource: "1"})
	var deletes []string
	for _, c := range plan.Changes {
		if c.Action == d.ActionDelete {
			deletes = append(deletes, c.Name)
		}
	}
	if len(deletes) != 1 || deletes[0] != "okapi" {
		t.Fatalf("scoped prune deleted the wrong set: %v (want only okapi)", deletes)
	}
}

func TestBuildPlanNoPruneKeepsOrphans(t *testing.T) {
	desired := d.NewResourceSet()
	desired.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindVolume, Metadata: d.Meta{Name: "keep"}, Volume: &d.VolumeSpec{}})
	actual := d.NewResourceSet()
	actual.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindVolume, Metadata: d.Meta{Name: "keep"}, Volume: &d.VolumeSpec{}})
	actual.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindVolume, Metadata: d.Meta{Name: "orphan"}, Volume: &d.VolumeSpec{}})

	plan := d.BuildPlan(desired, actual, d.PlanOptions{Prune: false})
	if _, _, del, _ := plan.Counts(); del != 0 {
		t.Errorf("prune off: want 0 deletes, got %d", del)
	}
}

func TestRenderInterpolatesContext(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{
		Databases: map[string]d.ConnView{"db": {URI: "postgres://u:p@mb-db-1/app"}},
		Secrets:   map[string]string{"app_key": "s3cret"},
	})
	env, err := r.RenderEnv("web", map[string]string{
		"DATABASE_URL": "{{ .databases.db.uri }}",
		"SECRET_KEY":   "{{ .secrets.app_key }}",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if env["DATABASE_URL"] != "postgres://u:p@mb-db-1/app" {
		t.Errorf("DATABASE_URL = %q", env["DATABASE_URL"])
	}
	if env["SECRET_KEY"] != "s3cret" {
		t.Errorf("SECRET_KEY = %q", env["SECRET_KEY"])
	}
}

func TestMarshalRoundTrips(t *testing.T) {
	set, err := d.Parse([]byte(projectYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := d.Marshal(set)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reparsed, err := d.Parse(bundle)
	if err != nil {
		t.Fatalf("reparse marshaled bundle: %v\n%s", err, bundle)
	}
	if reparsed.Len() != set.Len() {
		t.Fatalf("round-trip lost resources: %d -> %d", set.Len(), reparsed.Len())
	}
	app, ok := reparsed.Get("Application/web")
	if !ok || app.Application.Digest != "sha256:deadbeef" {
		t.Errorf("round-trip dropped application digest")
	}
}

func TestRenderMissingKeyIsError(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{})
	if _, err := r.RenderString("x", "{{ .databases.nope.uri }}"); err == nil {
		t.Fatal("expected error for missing key")
	}
}

// RenderEnvLenient resolves what it can and leaves the rest as templates, so a
// reference to a not-yet-provisioned database in the same bundle doesn't abort
// the plan (the apply path re-renders strictly once the database exists).
func TestRenderEnvLenient(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{
		Databases: map[string]d.ConnView{"live-db": {Host: "mb-live"}},
	})
	out := r.RenderEnvLenient("app", map[string]string{
		"RESOLVED": "{{ .databases.live-db.host }}",
		"PENDING":  "{{ .databases.future-db.host }}", // not provisioned yet
		"PLAIN":    "static",
	})
	if out["RESOLVED"] != "mb-live" {
		t.Errorf("resolved ref not interpolated: %q", out["RESOLVED"])
	}
	if out["PENDING"] != "{{ .databases.future-db.host }}" {
		t.Errorf("pending ref should be left as-is, got %q", out["PENDING"])
	}
	if out["PLAIN"] != "static" {
		t.Errorf("plain value changed: %q", out["PLAIN"])
	}
}

// Reference names commonly contain hyphens (e.g. "shop-db"), which Go's template
// language cannot access via dot notation. The renderer must resolve them.
func TestRenderHyphenatedRefs(t *testing.T) {
	r := d.NewRenderer(d.RenderContext{
		Databases: map[string]d.ConnView{"shop-db": {URI: "postgres://u:p@dp/shop", Host: "mb-db"}},
		Secrets:   map[string]string{"app-key": "s3cret"},
		Apps:      map[string]string{"shop-web": "mb-app-shop"},
	})
	cases := map[string]string{
		"{{ .databases.shop-db.uri }}":               "postgres://u:p@dp/shop",
		"{{ .databases.shop-db.host }}":              "mb-db",
		"{{ .databases.shop-db }}":                   "postgres://u:p@dp/shop", // bare name → URI
		"{{ .secrets.app-key }}":                     "s3cret",
		"{{ .applications.shop-web.alias }}":         "mb-app-shop",
		"jdbc:{{ .databases.shop-db.uri }}?ssl=true": "jdbc:postgres://u:p@dp/shop?ssl=true",
	}
	for in, want := range cases {
		got, err := r.RenderString("t", in)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	// A hyphenated reference to a missing resource must still error loudly.
	if _, err := r.RenderString("t", "{{ .databases.no-such.uri }}"); err == nil {
		t.Error("expected error for unknown hyphenated database")
	}
}

func TestMarshalRoundTripsUID(t *testing.T) {
	const uid = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	set := d.NewResourceSet()
	set.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindVolume,
		Metadata: d.Meta{Name: "data", UID: uid}, Volume: &d.VolumeSpec{},
	})
	out, err := d.Marshal(set)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Parse can only recover the uid if Marshal emitted it.
	back, err := d.Parse(out)
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, out)
	}
	if got := back.All()[0].Metadata.UID; got != uid {
		t.Errorf("uid not round-tripped: got %q, want %q", got, uid)
	}
}

func TestBuildPlanUIDMatchIsRenameSafe(t *testing.T) {
	const uid = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	// Desired references the volume by uid under a new name; actual has the same
	// uid under the old name. uid-matching must converge in place, never
	// delete+create (which would destroy the volume's data).
	desired := d.NewResourceSet()
	desired.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindVolume,
		Metadata: d.Meta{Name: "data-renamed", UID: uid}, Volume: &d.VolumeSpec{},
	})
	actual := d.NewResourceSet()
	actual.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindVolume,
		Metadata: d.Meta{Name: "data", UID: uid}, Volume: &d.VolumeSpec{},
	})
	c, _, del, _ := d.BuildPlan(desired, actual, d.PlanOptions{Prune: true}).Counts()
	if c != 0 || del != 0 {
		t.Errorf("uid match must be rename-safe; got create=%d delete=%d", c, del)
	}

	// Without a uid, the rename is a destructive delete+create (the old behavior).
	desiredNoUID := d.NewResourceSet()
	desiredNoUID.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindVolume,
		Metadata: d.Meta{Name: "data-renamed"}, Volume: &d.VolumeSpec{},
	})
	c2, _, del2, _ := d.BuildPlan(desiredNoUID, actual, d.PlanOptions{Prune: true}).Counts()
	if c2 != 1 || del2 != 1 {
		t.Errorf("name-only rename should be create+delete; got create=%d delete=%d", c2, del2)
	}
}
