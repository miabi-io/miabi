// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Goma drives Goma Gateway via its file provider: each route and middleware is written as a YAML
// file in Goma's watched directory, which Goma hot-reloads and merges. ACME is handled by Goma's
// global certManager; custom certs are inlined into the route's tls block.
type Goma struct {
	dir string
}

// NewGoma returns a Goma file-provider Manager writing into dir.
func NewGoma(dir string) *Goma { return &Goma{dir: dir} }

type gomaFile struct {
	Routes      []gomaRoute      `yaml:"routes,omitempty"`
	Middlewares []gomaMiddleware `yaml:"middlewares,omitempty"`
}

type gomaRoute struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
	// Enabled is a pointer so a disabled route emits `enabled: false`; nil omits
	// the key (Goma defaults to true) to keep enabled routes' files clean.
	Enabled     *bool            `yaml:"enabled,omitempty"`
	Hosts       []string         `yaml:"hosts,omitempty"`
	Methods     []string         `yaml:"methods,omitempty"`
	Rewrite     string           `yaml:"rewrite,omitempty"`
	Backends    []gomaBackend    `yaml:"backends"`
	Middlewares []string         `yaml:"middlewares,omitempty"`
	TLS         *gomaTLS         `yaml:"tls,omitempty"`
	Security    *gomaSecurity    `yaml:"security,omitempty"`
	Maintenance *gomaMaintenance `yaml:"maintenance,omitempty"`
}

func securityOf(route RenderedRoute) *gomaSecurity {
	sec := &gomaSecurity{}
	set := false
	for _, b := range route.Backends {
		if strings.HasPrefix(strings.ToLower(b.Endpoint), "https://") {
			sec.TLS = &gomaSecurityTLS{InsecureSkipVerify: true}
			set = true
			break
		}
	}
	if route.ExploitProtection {
		sec.EnableExploitProtection = true
		set = true
	}
	if !set {
		return nil
	}
	return sec
}

func mergeSecurity(existing any, resolved *gomaSecurity) (any, error) {
	base, ok := existing.(map[string]any)
	if !ok {
		return resolved, nil
	}
	out, err := yaml.Marshal(resolved)
	if err != nil {
		return nil, err
	}
	overlay := map[string]any{}
	if err := yaml.Unmarshal(out, &overlay); err != nil {
		return nil, err
	}
	for k, v := range overlay {
		if k == "tls" {
			if bt, ok := base["tls"].(map[string]any); ok {
				if ot, ok := v.(map[string]any); ok {
					for tk, tv := range ot {
						bt[tk] = tv
					}
					continue
				}
			}
		}
		base[k] = v
	}
	return base, nil
}

func maintenanceOf(route RenderedRoute) *gomaMaintenance {
	if route.Maintenance == nil || !route.Maintenance.Enabled {
		return nil
	}
	return &gomaMaintenance{
		Enabled:    true,
		StatusCode: route.Maintenance.StatusCode,
		Message:    route.Maintenance.Message,
	}
}

type gomaBackend struct {
	Endpoint  string      `yaml:"endpoint"`
	Weight    int         `yaml:"weight,omitempty"`
	Exclusive bool        `yaml:"exclusive,omitempty"`
	Priority  int         `yaml:"priority,omitempty"`
	Match     []gomaMatch `yaml:"match,omitempty"`
}

// gomaMatch is one canary match condition. Every field is required by the
// gateway, so none is omitempty — an incomplete rule must render visibly rather
// than silently collapse into a rule that matches everything.
type gomaMatch struct {
	Source   string `yaml:"source"`
	Name     string `yaml:"name"`
	Operator string `yaml:"operator"`
	Value    string `yaml:"value"`
}

// gomaTLS is a route's TLS block: either a named certManager provider serves the cert (the
// multi-provider certManager form, `tls.provider`), or an inline custom certificate is supplied
// under `tls.certificate` as base64 cert and key.
type gomaTLS struct {
	Provider    string    `yaml:"provider,omitempty"`
	Certificate *gomaCert `yaml:"certificate,omitempty"`
}

type gomaCert struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// gomaSecurity is the route's security block. We use it only to skip TLS
// verification for HTTPS backends reached over an internal Docker alias (whose
// self-signed cert can't be verified against the alias hostname).
type gomaSecurity struct {
	EnableExploitProtection bool             `yaml:"enableExploitProtection,omitempty"`
	TLS                     *gomaSecurityTLS `yaml:"tls,omitempty"`
}
type gomaSecurityTLS struct {
	InsecureSkipVerify bool `yaml:"insecureSkipVerify"`
}

type gomaMaintenance struct {
	Enabled    bool   `yaml:"enabled"`
	StatusCode int    `yaml:"statusCode,omitempty"`
	Message    string `yaml:"message,omitempty"`
}

type gomaMiddleware struct {
	Name  string   `yaml:"name"`
	Type  string   `yaml:"type"`
	Paths []string `yaml:"paths,omitempty"`
	// Rule is the middleware's rule mapping, or — when config encryption is
	// enabled — an encrypted scalar string that Goma decrypts and decodes at load.
	Rule interface{} `yaml:"rule,omitempty"`
}

// newGomaMiddleware builds a middleware definition, encrypting its rule when a
// shared config-encryption key is configured.
func newGomaMiddleware(mw RenderedMiddleware) (gomaMiddleware, error) {
	rule, err := renderRule(mw.Rule)
	if err != nil {
		return gomaMiddleware{}, fmt.Errorf("middleware %q rule: %w", mw.Name, err)
	}
	return gomaMiddleware{
		Name:  gomaName(mw.WorkspaceID, mw.Name),
		Type:  mw.Type,
		Paths: mwPaths(mw.Paths),
		Rule:  rule,
	}, nil
}

// workspacePath is the single file holding all of a workspace's routes and middlewares. Keeping a
// route and the middlewares it references in one file lets Goma resolve the references atomically
// on reload — separate files reload independently, so a route could load before its middleware.
func (g *Goma) workspacePath(id uint) string {
	return filepath.Join(g.dir, fmt.Sprintf("mb-ws%d.yml", id))
}

const fileHeader = "# Managed by Miabi. Do not edit by hand.\n"

// gomaName builds the globally-unique Goma identifier for a workspace-scoped route or middleware:
// mb-ws<workspaceID>-<slug(name)>. Goma rejects duplicate names across the merged config, and
// route->middleware references are namespaced the same way, so the references still match.
func gomaName(workspaceID uint, name string) string {
	return fmt.Sprintf("mb-ws%d-%s", workspaceID, slugify(name))
}

// GomaName exposes the Goma identifier a workspace route is served under, so
// consumers of the gateway's telemetry (the analytics stream carries this name)
// can map an event back to the route it belongs to.
func GomaName(workspaceID uint, name string) string {
	return gomaName(workspaceID, name)
}

// slugify lowercases name and collapses any run of non-alphanumeric characters
// to a single hyphen (trimmed), producing a Goma/DNS-safe token.
func slugify(s string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if !hyphen && b.Len() > 0 {
			b.WriteByte('-')
			hyphen = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func mwRefs(route RenderedRoute) []string {
	if len(route.Middlewares) == 0 {
		return nil
	}
	out := make([]string, 0, len(route.Middlewares))
	for _, m := range route.Middlewares {
		out = append(out, gomaName(route.WorkspaceID, m))
	}
	return out
}

// prefixAdvancedMwRefs namespaces the `middlewares` list inside an advanced route
// YAML (a list of plain names the admin typed) to their Goma names.
func prefixAdvancedMwRefs(workspaceID uint, v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, gomaName(workspaceID, s))
		} else {
			out = append(out, e)
		}
	}
	return out
}

func backendsOf(route RenderedRoute) []gomaBackend {
	backends := make([]gomaBackend, 0, len(route.Backends))
	for _, b := range route.Backends {
		backends = append(backends, gomaBackend{
			Endpoint:  b.Endpoint,
			Weight:    b.Weight,
			Exclusive: b.Exclusive,
			Priority:  b.Priority,
			Match:     matchesOf(b.Match),
		})
	}
	return backends
}

// matchesOf converts a backend's match rules, returning nil for an empty set so
// the rendered backend keeps its ordinary (match-less) shape.
func matchesOf(rules []MatchRule) []gomaMatch {
	if len(rules) == 0 {
		return nil
	}
	out := make([]gomaMatch, 0, len(rules))
	for _, r := range rules {
		out = append(out, gomaMatch(r))
	}
	return out
}

// tlsOf returns the route's TLS block: an inline custom certificate when supplied, otherwise a
// named certManager provider (or nil, leaving the gateway's default). Only the first cert is used,
// and cert/key are base64-encoded so multi-line PEM never has to be a YAML literal block.
func tlsOf(route RenderedRoute) (*gomaTLS, error) {
	// An explicit opt-out: tell the gateway not to manage a cert for this route.
	if route.TLSNone {
		return &gomaTLS{Provider: "none"}, nil
	}
	if len(route.Certs) > 0 {
		c := route.Certs[0]
		cert := base64.StdEncoding.EncodeToString([]byte(c.CertPEM))
		key := base64.StdEncoding.EncodeToString([]byte(c.KeyPEM))
		// Encrypt the inline certificate material so it is never written to the
		// provider directory in the clear; Goma decrypts it at load.
		if encryptionEnabled() {
			var err error
			if cert, err = encryptField(cert); err != nil {
				return nil, fmt.Errorf("encrypt certificate for route %q: %w", route.Name, err)
			}
			if key, err = encryptField(key); err != nil {
				return nil, fmt.Errorf("encrypt key for route %q: %w", route.Name, err)
			}
		}
		return &gomaTLS{
			Provider:    route.TLSProvider,
			Certificate: &gomaCert{Cert: cert, Key: key},
		}, nil
	}
	if route.TLSProvider != "" {
		return &gomaTLS{Provider: route.TLSProvider}, nil
	}
	return nil, nil
}

// gomaRouteValue returns the YAML-marshalable route entry: the structured gomaRoute for a simple
// route, or the admin's raw YAML with name/backends/tls forced by Miabi, so the route always
// carries its own identity and points at the app rather than a hand-typed upstream.
func gomaRouteValue(route RenderedRoute) (any, error) {
	backends := backendsOf(route)
	tls, err := tlsOf(route)
	if err != nil {
		return nil, err
	}
	security := securityOf(route)
	name := gomaName(route.WorkspaceID, route.Name)
	if strings.TrimSpace(route.AdvancedYAML) != "" {
		m := map[string]any{}
		if err := yaml.Unmarshal([]byte(route.AdvancedYAML), &m); err != nil {
			return nil, fmt.Errorf("advanced route config %q: %w", route.Name, err)
		}
		m["name"] = name
		bl := make([]map[string]any, 0, len(backends))
		for _, b := range backends {
			e := map[string]any{"endpoint": b.Endpoint}
			if b.Weight != 0 {
				e["weight"] = b.Weight
			}
			if b.Exclusive {
				e["exclusive"] = true
			}
			if b.Priority != 0 {
				e["priority"] = b.Priority
			}
			if len(b.Match) > 0 {
				ml := make([]map[string]any, 0, len(b.Match))
				for _, m := range b.Match {
					ml = append(ml, map[string]any{"source": m.Source, "name": m.Name, "operator": m.Operator, "value": m.Value})
				}
				e["match"] = ml
			}
			bl = append(bl, e)
		}
		m["backends"] = bl
		delete(m, "target") // single-target shorthand is ignored; injected backends win
		if mws, ok := m["middlewares"]; ok {
			m["middlewares"] = prefixAdvancedMwRefs(route.WorkspaceID, mws)
		}
		// Miabi fully owns TLS (managed, domain-validated certs). Never honor a tls block hand-typed in
		// the advanced config — a route could otherwise smuggle a certificate for a host it doesn't
		// control. Drop it, then apply only Miabi's resolved TLS.
		delete(m, "tls")
		if tls != nil {
			m["tls"] = tls
		}
		if security != nil {
			merged, err := mergeSecurity(m["security"], security)
			if err != nil {
				return nil, fmt.Errorf("advanced route config %q: %w", route.Name, err)
			}
			m["security"] = merged
		}
		if mt := maintenanceOf(route); mt != nil {
			m["maintenance"] = mt
		}
		// A disabled route is force-marked so the gateway stops serving it,
		// regardless of any hand-typed value.
		if route.Disabled {
			m["enabled"] = false
		}
		if _, ok := m["path"]; !ok {
			m["path"] = "/"
		}
		return m, nil
	}
	path := route.Path
	if path == "" {
		path = "/"
	}
	// Defence in depth: a structured route with no host matches every request on its path, a catch-all
	// that swallows all traffic. The serve-state gate already marks it disabled, but force
	// enabled:false here too so the renderer can never emit a live hostless route.
	enabled := enabledField(route)
	if len(route.Hosts) == 0 {
		off := false
		enabled = &off
	}
	return gomaRoute{
		Name: name, Path: path, Enabled: enabled, Hosts: route.Hosts, Methods: route.Methods,
		Rewrite: route.Rewrite, Backends: backends, Middlewares: mwRefs(route), TLS: tls, Security: security,
		Maintenance: maintenanceOf(route),
	}, nil
}

// enabledField renders `enabled: false` for a disabled route, or nil (omitted;
// Goma defaults to true) for an enabled one.
func enabledField(route RenderedRoute) *bool {
	if route.Disabled {
		v := false
		return &v
	}
	return nil
}

// RenderRoute produces the YAML file content for a route (exported for tests).
func RenderRoute(route RenderedRoute) ([]byte, error) {
	rv, err := gomaRouteValue(route)
	if err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(map[string]any{"routes": []any{rv}})
	if err != nil {
		return nil, err
	}
	return append([]byte(fileHeader), body...), nil
}

// RenderBundle produces a combined routes+middlewares Goma config document
// (the shape Goma's HTTP provider consumes). Used to serve a remote node's Goma
// over the control plane's HTTP provider endpoint.
func RenderBundle(routes []RenderedRoute, mws []RenderedMiddleware) ([]byte, error) {
	doc := map[string]any{}
	routeList := make([]any, 0, len(routes))
	for _, route := range routes {
		rv, err := gomaRouteValue(route)
		if err != nil {
			return nil, err
		}
		routeList = append(routeList, rv)
	}
	if len(routeList) > 0 {
		doc["routes"] = routeList
	}
	mwList := make([]gomaMiddleware, 0, len(mws))
	for _, mw := range mws {
		m, err := newGomaMiddleware(mw)
		if err != nil {
			return nil, err
		}
		mwList = append(mwList, m)
	}
	if len(mwList) > 0 {
		doc["middlewares"] = mwList
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte(fileHeader), body...), nil
}

// mwPaths returns the middleware's request-path scope, defaulting to "/.*" (all
// paths) when none is set.
func mwPaths(paths []string) []string {
	if len(paths) == 0 {
		return []string{"/.*"}
	}
	return paths
}

// RenderMiddleware produces the YAML file content for a middleware.
func RenderMiddleware(mw RenderedMiddleware) ([]byte, error) {
	m, err := newGomaMiddleware(mw)
	if err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(gomaFile{Middlewares: []gomaMiddleware{m}})
	if err != nil {
		return nil, err
	}
	return append([]byte(fileHeader), body...), nil
}

func (g *Goma) writeFile(path string, content []byte) error {
	if err := os.MkdirAll(g.dir, 0o755); err != nil {
		return fmt.Errorf("proxy: create provider dir: %w", err)
	}
	// A unique temp name (not a shared "<path>.tmp") so two concurrent syncs of the
	// same file can't interleave into one temp and publish truncated content via
	// the rename. Each writer stages its own complete file, then renames atomically.
	tmp, err := os.CreateTemp(g.dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("proxy: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("proxy: write file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("proxy: chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("proxy: close temp file: %w", err)
	}
	return os.Rename(tmpName, path) // atomic
}

func remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SyncWorkspace writes (or removes) a workspace's single Goma config file holding
// all its routes and middlewares. The file is replaced atomically, so Goma always
// reads a route and the middlewares it references together.
func (g *Goma) SyncWorkspace(_ context.Context, workspaceID uint, routes []RenderedRoute, mws []RenderedMiddleware) error {
	if len(routes) == 0 && len(mws) == 0 {
		return remove(g.workspacePath(workspaceID))
	}
	content, err := RenderBundle(routes, mws)
	if err != nil {
		return err
	}
	return g.writeFile(g.workspacePath(workspaceID), content)
}

// registryPath is the dedicated file holding the built-in registry's route and
// middlewares (kept apart from the per-workspace files).
func (g *Goma) registryPath() string { return filepath.Join(g.dir, "mb-registry.yml") }

// SyncRegistry writes the built-in registry's gateway config (route + HTTPS
// redirect, forwardAuth, and namespace-rewrite middlewares) to its own file, or
// removes the file when disabled / incompletely configured. Idempotent.
func (g *Goma) SyncRegistry(_ context.Context, cfg RegistryProxy) error {
	if !cfg.Enabled || cfg.Host == "" || cfg.Upstream == "" || cfg.AuthURL == "" {
		return remove(g.registryPath())
	}
	var tls *gomaTLS
	if cfg.TLSProvider != "" {
		tls = &gomaTLS{Provider: cfg.TLSProvider}
	}
	file := gomaFile{
		Routes: []gomaRoute{{
			Name:     "mb-registry",
			Path:     "/",
			Hosts:    []string{cfg.Host},
			Backends: []gomaBackend{{Endpoint: cfg.Upstream}},
			// mb-registry-nomount runs BEFORE the auth check on purpose — see its
			// definition below.
			Middlewares: []string{"mb-registry-https", "mb-registry-nomount", "mb-registry-auth", "mb-registry-ns-rewrite"},
			TLS:         tls,
		}},
		Middlewares: []gomaMiddleware{
			{
				Name: "mb-registry-https",
				Type: "redirectScheme",
				Rule: map[string]any{"scheme": "https", "port": 443, "permanent": true},
			},
			{
				// Drop the cross-repository blob mount hint. A docker push offers `?mount=<digest>&from=<repo>` to link a
				// layer the daemon has seen in another repository instead of uploading it.
				Name:  "mb-registry-nomount",
				Type:  "stripQuery",
				Paths: []string{"/.*"},
				Rule: map[string]any{
					"params":      []string{"mount", "from"},
					"methods":     []string{"POST"},
					"pathPattern": "^/v2/.+/blobs/uploads/?$",
				},
			},
			{
				// forwardAuth → Miabi: authorizes the request and returns the
				// X-Miabi-Registry-Namespace (ws_<id>) header copied onto the request.
				Name:  "mb-registry-auth",
				Type:  "forwardAuth",
				Paths: []string{"/.*"},
				Rule: map[string]any{
					"authUrl":             cfg.AuthURL,
					"authResponseHeaders": []string{"X-Miabi-Registry-Namespace"},
				},
			},
			{
				// Rewrite the workspace-name segment to the immutable id namespace.
				Name:  "mb-registry-ns-rewrite",
				Type:  "rewriteRegex",
				Paths: []string{"/.*"},
				Rule: map[string]any{
					"pattern":     "^/v2/[^/]+/(.*)",
					"replacement": "/v2/{{goma.headers.X-Miabi-Registry-Namespace}}/$1",
				},
			},
		},
	}
	content, err := yaml.Marshal(file)
	if err != nil {
		return fmt.Errorf("proxy: render registry config: %w", err)
	}
	return g.writeFile(g.registryPath(), append([]byte(fileHeader), content...))
}
