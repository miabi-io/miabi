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

// Goma is driven via its file provider, which hot-reloads YAML in its watched directory.
type Goma struct {
	dir string
	// Central gateway's passphrase: nodes are served by RenderBundle under their own key.
	key string
}

func NewGoma(dir string) *Goma { return &Goma{dir: dir, key: CentralConfigKey()} }

type gomaFile struct {
	Routes      []gomaRoute      `yaml:"routes,omitempty"`
	Middlewares []gomaMiddleware `yaml:"middlewares,omitempty"`
}

type gomaRoute struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
	// Pointer so a disabled route emits `enabled: false`; nil omits it (Goma defaults to true).
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

// Not omitempty: an incomplete rule must render visibly rather than collapse into a match-everything rule.
type gomaMatch struct {
	Source   string `yaml:"source"`
	Name     string `yaml:"name"`
	Operator string `yaml:"operator"`
	Value    string `yaml:"value"`
}

type gomaTLS struct {
	Provider    string    `yaml:"provider,omitempty"`
	Certificate *gomaCert `yaml:"certificate,omitempty"`
}

type gomaCert struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// Only used to skip TLS verification for HTTPS backends reached by Docker alias, whose cert cannot match the alias.
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
	// An encrypted scalar when config encryption is enabled.
	Rule interface{} `yaml:"rule,omitempty"`
}

// newGomaMiddleware builds a middleware definition, encrypting its rule under key when one is set.
func newGomaMiddleware(mw RenderedMiddleware, key string) (gomaMiddleware, error) {
	rule, err := renderRule(mw.Rule, key)
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

// One file per workspace so Goma resolves route-to-middleware references atomically on reload.
func (g *Goma) workspacePath(id uint) string {
	return filepath.Join(g.dir, fmt.Sprintf("mb-ws%d.yml", id))
}

const fileHeader = "# Managed by Miabi. Do not edit by hand.\n"

// Goma rejects duplicate names across the merged config, so names are workspace-namespaced.
func gomaName(workspaceID uint, name string) string {
	return fmt.Sprintf("mb-ws%d-%s", workspaceID, slugify(name))
}

// Lets analytics consumers map a gateway event back to its route.
func GomaName(workspaceID uint, name string) string {
	return gomaName(workspaceID, name)
}

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

// nil keeps the rendered backend in its ordinary match-less shape.
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

// Cert and key are base64-encoded so multi-line PEM never needs a YAML literal block; only the first cert is used.
func tlsOf(route RenderedRoute, cfgKey string) (*gomaTLS, error) {
	// An explicit opt-out: tell the gateway not to manage a cert for this route.
	if route.TLSNone {
		return &gomaTLS{Provider: "none"}, nil
	}
	if len(route.Certs) > 0 {
		c := route.Certs[0]
		cert := base64.StdEncoding.EncodeToString([]byte(c.CertPEM))
		key := base64.StdEncoding.EncodeToString([]byte(c.KeyPEM))
		// Goma decrypts it at load.
		if cfgKey != "" {
			var err error
			if cert, err = encryptField(cert, cfgKey); err != nil {
				return nil, fmt.Errorf("encrypt certificate for route %q: %w", route.Name, err)
			}
			if key, err = encryptField(key, cfgKey); err != nil {
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

// Miabi forces name, backends and tls so an advanced route always points at the app, not a hand-typed upstream.
func gomaRouteValue(route RenderedRoute, cfgKey string) (any, error) {
	backends := backendsOf(route)
	tls, err := tlsOf(route, cfgKey)
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
		// Never honor a hand-typed tls block: a route could smuggle a certificate for a host it does not control.
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
		// Overwrite, not merge: stored configs must not claim another tenant's hostname or outrank the console with a longer path.
		m["hosts"] = route.Hosts
		path := route.Path
		if path == "" {
			path = "/"
		}
		m["path"] = path
		delete(m, "priority")
		if route.Disabled {
			m["enabled"] = false
		}
		// A route with no host matches every request on its path.
		if len(route.Hosts) == 0 {
			m["enabled"] = false
		}
		return m, nil
	}
	path := route.Path
	if path == "" {
		path = "/"
	}
	// Defence in depth: a hostless route is a catch-all, so never emit one live even if the serve gate missed it.
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

func enabledField(route RenderedRoute) *bool {
	if route.Disabled {
		v := false
		return &v
	}
	return nil
}

func RenderRoute(route RenderedRoute) ([]byte, error) {
	rv, err := gomaRouteValue(route, CentralConfigKey())
	if err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(map[string]any{"routes": []any{rv}})
	if err != nil {
		return nil, err
	}
	return append([]byte(fileHeader), body...), nil
}

// cfgKey is that node's own key, so a bundle escaping one host is unreadable on any other; empty writes plaintext.
func RenderBundle(routes []RenderedRoute, mws []RenderedMiddleware, cfgKey string) ([]byte, error) {
	doc := map[string]any{}
	routeList := make([]any, 0, len(routes))
	for _, route := range routes {
		rv, err := gomaRouteValue(route, cfgKey)
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
		m, err := newGomaMiddleware(mw, cfgKey)
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

func mwPaths(paths []string) []string {
	if len(paths) == 0 {
		return []string{"/.*"}
	}
	return paths
}

// RenderMiddleware produces the YAML file content for a middleware, under the central gateway's key.
func RenderMiddleware(mw RenderedMiddleware) ([]byte, error) {
	m, err := newGomaMiddleware(mw, CentralConfigKey())
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
	// A unique temp name so concurrent syncs cannot interleave into one temp and publish truncated content.
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

// Replaced atomically so Goma always reads a route together with the middlewares it references.
func (g *Goma) SyncWorkspace(_ context.Context, workspaceID uint, routes []RenderedRoute, mws []RenderedMiddleware) error {
	if len(routes) == 0 && len(mws) == 0 {
		return remove(g.workspacePath(workspaceID))
	}
	// The provider directory is the CENTRAL gateway's, so it is written under its key.
	content, err := RenderBundle(routes, mws, g.key)
	if err != nil {
		return err
	}
	return g.writeFile(g.workspacePath(workspaceID), content)
}

func (g *Goma) registryPath() string { return filepath.Join(g.dir, "mb-registry.yml") }

// Removes the file when disabled or incompletely configured; idempotent.
func (g *Goma) SyncRegistry(_ context.Context, cfg RegistryProxy) error {
	if !cfg.Enabled || cfg.Host == "" || cfg.Upstream == "" || cfg.AuthURL == "" {
		return remove(g.registryPath())
	}
	var tls *gomaTLS
	if cfg.TLSProvider != "" {
		tls = &gomaTLS{Provider: cfg.TLSProvider}
	}
	// Omitted rather than rendered unreferenced: an orphan middleware puzzles whoever reads the file next.
	mwNames := []string{"mb-registry-nomount", "mb-registry-auth", "mb-registry-ns-rewrite"}
	var mws []gomaMiddleware
	// Must be last: mb-registry-auth reads the tenant's header, so replacing it earlier would let anyone through.
	if cfg.UpstreamAuth != "" {
		mwNames = append(mwNames, "mb-registry-upstream-auth")
		mws = append(mws, gomaMiddleware{
			Name:  "mb-registry-upstream-auth",
			Type:  "requestHeaders",
			Paths: []string{"/.*"},
			Rule:  map[string]any{"setHeaders": map[string]string{"Authorization": cfg.UpstreamAuth}},
		})
	}
	if cfg.HTTPSRedirect {
		mwNames = append([]string{"mb-registry-https"}, mwNames...)
		mws = append(mws, gomaMiddleware{
			Name: "mb-registry-https",
			Type: "redirectScheme",
			Rule: map[string]any{"scheme": "https", "port": 443, "permanent": true},
		})
	}
	file := gomaFile{
		Routes: []gomaRoute{{
			Name:     "mb-registry",
			Path:     "/",
			Hosts:    []string{cfg.Host},
			Backends: []gomaBackend{{Endpoint: cfg.Upstream}},
			// mb-registry-nomount runs before the auth check on purpose; see its definition below.
			Middlewares: mwNames,
			TLS:         tls,
		}},
		Middlewares: append(mws, []gomaMiddleware{
			{
				// Strips the ?mount=&from= hint a push uses to link a layer from another repository instead of uploading it.
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
		}...),
	}
	content, err := yaml.Marshal(file)
	if err != nil {
		return fmt.Errorf("proxy: render registry config: %w", err)
	}
	return g.writeFile(g.registryPath(), append([]byte(fileHeader), content...))
}
