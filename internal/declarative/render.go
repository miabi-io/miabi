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

package declarative

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

type ConnView struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	URI      string
	// The read-only user, when the database has one (readOnlyUser: true); empty otherwise.
	ReadOnlyUser     string
	ReadOnlyPassword string
	ReadOnlyURI      string
	PasswordSecret   string
	URISecret        string
}

// fields is the view's {{ .databases.<name>.<field> }} map.
func (c ConnView) fields() map[string]string {
	return map[string]string{
		"host": c.Host, "port": c.Port, "user": c.User,
		"password": c.Password, "name": c.Name, "uri": c.URI,
		"url":           c.URI, // alias of "uri"
		"readonly_user": c.ReadOnlyUser, "readonly_password": c.ReadOnlyPassword,
		"readonly_uri": c.ReadOnlyURI, "readonly_url": c.ReadOnlyURI,
	}
}

// Host is the app's own name, aliased on every workspace network; Alias predates it and Config files use it.
type AppView struct {
	Host   string
	Port   string
	Scheme string
	Alias  string
}

// URL omits the port when none is declared, rather than emitting a trailing colon.
func (a AppView) URL() string {
	scheme := a.Scheme
	if scheme == "" {
		scheme = "http"
	}
	if a.Port == "" || a.Port == "0" {
		return scheme + "://" + a.Host
	}
	return scheme + "://" + a.Host + ":" + a.Port
}

// RenderContext adds secrets to the marketplace context so GitOps and apply share one interpolation surface.
type RenderContext struct {
	Inputs    map[string]string
	Databases map[string]ConnView
	Apps      map[string]AppView // app name -> how a sibling addresses it
	Secrets   map[string]string  // secret name -> value
}

// Renderer interpolates manifest strings against a RenderContext.
type Renderer struct {
	ctx RenderContext
	// credential holds keys whose value resolved a credential rather than deferring it; see CredentialKeys.
	cur        string
	credential map[string]bool
}

// NewRenderer builds a renderer (nil maps are tolerated).
func NewRenderer(ctx RenderContext) *Renderer {
	if ctx.Inputs == nil {
		ctx.Inputs = map[string]string{}
	}
	if ctx.Databases == nil {
		ctx.Databases = map[string]ConnView{}
	}
	if ctx.Apps == nil {
		ctx.Apps = map[string]AppView{}
	}
	if ctx.Secrets == nil {
		ctx.Secrets = map[string]string{}
	}
	return &Renderer{ctx: ctx}
}

func (r *Renderer) data() map[string]any {
	dbs := map[string]map[string]string{}
	for name, c := range r.ctx.Databases {
		dbs[name] = c.fields()
	}
	apps := map[string]map[string]string{}
	for name, a := range r.ctx.Apps {
		apps[name] = map[string]string{
			"host": a.Host, "port": a.Port, "scheme": a.Scheme,
			"url": a.URL(),
			// Predates host and may already be referenced by a Config, so it keeps resolving.
			"alias": a.Alias,
		}
	}
	return map[string]any{
		"inputs":       r.ctx.Inputs,
		"databases":    dbs,
		"applications": apps,
		"secrets":      r.ctx.Secrets,
	}
}

var funcMap = template.FuncMap{
	"randAlphaNum": RandAlphaNum,
	"randHex":      randHex,
	"base64":       func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) },
	"default": func(def, v string) string {
		if strings.TrimSpace(v) == "" {
			return def
		}
		return v
	},
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
}

// actionPattern bounds the rewrite to inside "{{ ... }}" so literal text is never touched.
var (
	refPattern    = regexp.MustCompile(`\.(databases|secrets|applications|inputs)((?:\.[A-Za-z0-9_-]+)+)`)
	actionPattern = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
)

// A piped or wrapped reference cannot be deferred, since the worker only substitutes.
var (
	pureSecretAction   = regexp.MustCompile(`^\{\{(-?\s*)\.secrets\.([A-Za-z][A-Za-z0-9_-]{0,62})(\s*-?)\}\}$`)
	pureDatabaseAction = regexp.MustCompile(`^\{\{(-?\s*)\.databases\.([A-Za-z0-9_-]+)(?:\.(password|uri|url))?(\s*-?)\}\}$`)
	runtimeSecretRef   = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Za-z][A-Za-z0-9_-]{0,62})\s*\}\}`)
)

// credentialFields are the database fields whose value is a credential.
var credentialFields = map[string]bool{
	"password": true, "uri": true, "url": true,
	"readonly_password": true, "readonly_uri": true, "readonly_url": true,
}

// The stored env points at the vault so it follows secret rotations.
func deferSecrets(s string) string {
	s = runtimeSecretRef.ReplaceAllString(s, `{{ secretRef "$1" }}`)
	return actionPattern.ReplaceAllStringFunc(s, func(action string) string {
		if m := pureSecretAction.FindStringSubmatch(action); m != nil {
			return `{{` + m[1] + `secretRef "` + m[2] + `"` + m[3] + `}}`
		}
		if m := pureDatabaseAction.FindStringSubmatch(action); m != nil {
			return `{{` + m[1] + `databaseSecretRef "` + m[2] + `" "` + m[3] + `"` + m[4] + `}}`
		}
		return action
	})
}

// Go templates cannot access ".databases.shop-db", so dotted refs are rewritten into `ref` calls.
func expandRefs(s string) string {
	return expandRefsIn(s, actionPattern)
}

// The pattern must use the config's delimiters, or hyphenated names inside << >> never resolve.
func expandRefsDelims(s, left, right string) string {
	if left == "{{" && right == "}}" {
		return expandRefs(s)
	}
	pat, err := regexp.Compile(`(?s)` + regexp.QuoteMeta(left) + `.*?` + regexp.QuoteMeta(right))
	if err != nil {
		return s
	}
	return expandRefsIn(s, pat)
}

func expandRefsIn(s string, action *regexp.Regexp) string {
	return action.ReplaceAllStringFunc(s, func(action string) string {
		return refPattern.ReplaceAllStringFunc(action, func(m string) string {
			sub := refPattern.FindStringSubmatch(m)
			var b strings.Builder
			b.WriteString(`(ref "`)
			b.WriteString(sub[1]) // collection
			b.WriteByte('"')
			for _, seg := range strings.Split(strings.Trim(sub[2], "."), ".") {
				b.WriteString(` "`)
				b.WriteString(seg)
				b.WriteByte('"')
			}
			b.WriteByte(')')
			return b.String()
		})
	})
}

// Errors on unknown names so a typo or unprovisioned dependency fails loudly instead of rendering empty.
func (r *Renderer) ref(coll string, keys ...string) (string, error) {
	if len(keys) == 0 {
		return "", fmt.Errorf("reference .%s needs a name", coll)
	}
	name := keys[0]
	field := ""
	if len(keys) > 1 {
		field = keys[1]
	}
	if coll == "secrets" || (coll == "databases" && (field == "" || credentialFields[field])) {
		r.markCredential()
	}
	switch coll {
	case "inputs":
		v, ok := r.ctx.Inputs[name]
		if !ok {
			return "", fmt.Errorf("unknown input %q", name)
		}
		return v, nil
	case "secrets":
		v, ok := r.ctx.Secrets[name]
		if !ok {
			return "", fmt.Errorf("unknown secret %q", name)
		}
		return v, nil
	case "databases":
		c, ok := r.ctx.Databases[name]
		if !ok {
			return "", fmt.Errorf("unknown database %q", name)
		}
		fields := c.fields()
		if field == "" {
			field = "uri" // the most common reference; ".databases.x" alone means its URI
		}
		v, ok := fields[field]
		if !ok {
			return "", fmt.Errorf("database %q has no field %q", name, field)
		}
		if v == "" && strings.HasPrefix(field, "readonly_") {
			return "", fmt.Errorf("database %q has no read-only user: set readOnlyUser: true on it", name)
		}
		return v, nil
	case "applications":
		a, ok := r.ctx.Apps[name]
		if !ok {
			return "", fmt.Errorf("unknown application %q", name)
		}
		fields := map[string]string{
			"host": a.Host, "port": a.Port, "scheme": a.Scheme,
			"url": a.URL(), "alias": a.Alias,
		}
		if field == "" {
			field = "url" // ".applications.x" alone means the address to dial, as ".databases.x" means its URI
		}
		v, ok := fields[field]
		if !ok {
			return "", fmt.Errorf("application %q has no field %q", name, field)
		}
		return v, nil
	}
	return "", fmt.Errorf("unknown reference collection %q", coll)
}

// Checks existence so a typo fails the apply rather than the deploy.
func (r *Renderer) secretRef(name string) (string, error) {
	if _, ok := r.ctx.Secrets[name]; !ok {
		return "", fmt.Errorf("unknown secret %q", name)
	}
	return "${{ secrets." + name + " }}", nil
}

// Falls back to the value when the database has no vault secret yet.
func (r *Renderer) databaseSecretRef(name, field string) (string, error) {
	c, ok := r.ctx.Databases[name]
	if !ok {
		return "", fmt.Errorf("unknown database %q", name)
	}
	secret := c.URISecret
	if field == "password" {
		secret = c.PasswordSecret
	}
	if _, inVault := r.ctx.Secrets[secret]; secret != "" && inVault {
		return "${{ secrets." + secret + " }}", nil
	}
	if field == "" {
		return r.ref("databases", name)
	}
	return r.ref("databases", name, field)
}

func (r *Renderer) markCredential() {
	if r.cur != "" && r.credential != nil {
		r.credential[r.cur] = true
	}
}

// CredentialKeys must be stored encrypted, whatever secretEnv says.
func (r *Renderer) CredentialKeys() []string {
	out := make([]string, 0, len(r.credential))
	for k := range r.credential {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *Renderer) funcs() template.FuncMap {
	fm := make(template.FuncMap, len(funcMap)+3)
	for k, v := range funcMap {
		fm[k] = v
	}
	fm["ref"] = r.ref
	fm["secretRef"] = r.secretRef
	fm["databaseSecretRef"] = r.databaseSecretRef
	return fm
}

func (r *Renderer) RenderString(name, s string) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	t, err := template.New(name).Funcs(r.funcs()).Option("missingkey=error").Parse(expandRefs(s))
	if err != nil {
		return "", fmt.Errorf("template %s: %w", name, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, r.data()); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return b.String(), nil
}

// RenderFiles Delimiters replace the markers for this config only, so files using {{ }} are not mangled.
func (r *Renderer) RenderFiles(scope string, files map[string]string, delimiters []string) (map[string]string, error) {
	left, right := "{{", "}}"
	if len(delimiters) == 2 && delimiters[0] != "" && delimiters[1] != "" {
		left, right = delimiters[0], delimiters[1]
	}
	out := make(map[string]string, len(files))
	for k, v := range files {
		if !strings.Contains(v, left) {
			out[k] = v
			continue
		}
		name := scope + "." + k
		t, err := template.New(name).Delims(left, right).Funcs(r.funcs()).Option("missingkey=error").Parse(expandRefsDelims(v, left, right))
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		var b strings.Builder
		if err := t.Execute(&b, r.data()); err != nil {
			return nil, fmt.Errorf("render %s: %w", name, err)
		}
		out[k] = b.String()
	}
	return out, nil
}

// RenderEnv keeps secrets and database credentials as `${{ secrets.NAME }}` pointers resolved at deploy.
func (r *Renderer) RenderEnv(scope string, env map[string]string) (map[string]string, error) {
	r.credential = map[string]bool{}
	defer func() { r.cur = "" }()
	out := make(map[string]string, len(env))
	for k, v := range env {
		r.cur = k
		rv, err := r.RenderString(scope+".env."+k, deferSecrets(v))
		if err != nil {
			return nil, err
		}
		out[k] = rv
	}
	return out, nil
}

// RenderEnvLenient leaves unresolvable values as templates for plan/diff; apply re-renders strictly.
func (r *Renderer) RenderEnvLenient(scope string, env map[string]string) map[string]string {
	r.credential = map[string]bool{}
	defer func() { r.cur = "" }()
	out := make(map[string]string, len(env))
	for k, v := range env {
		r.cur = k
		if rv, err := r.RenderString(scope+".env."+k, deferSecrets(v)); err == nil {
			out[k] = rv
		} else {
			// Left as its template, so it holds no credential yet.
			delete(r.credential, k)
			out[k] = v
		}
	}
	return out
}

const alphanum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func RandAlphaNum(n int) string {
	if n <= 0 {
		n = 16
	}
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphanum)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			b[i] = alphanum[i%len(alphanum)]
			continue
		}
		b[i] = alphanum[idx.Int64()]
	}
	return string(b)
}

func randHex(n int) string {
	if n <= 0 {
		n = 16
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return fmt.Sprintf("%x", b)
}
