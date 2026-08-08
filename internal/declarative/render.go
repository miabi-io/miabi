// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"text/template"
)

// ConnView is a resolved database's connection detail, exposed to env
// interpolation as {{ .databases.<name>.* }}.
type ConnView struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	URI      string
}

// RenderContext is the read-only data env interpolation resolves against. It
// mirrors the marketplace renderer's context but adds secrets, so GitOps and
// apply share one interpolation surface.
type RenderContext struct {
	Inputs    map[string]string
	Databases map[string]ConnView
	Apps      map[string]string // app name -> network alias
	Secrets   map[string]string // secret name -> value
}

// Renderer interpolates manifest strings against a RenderContext.
type Renderer struct {
	ctx RenderContext
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
		ctx.Apps = map[string]string{}
	}
	if ctx.Secrets == nil {
		ctx.Secrets = map[string]string{}
	}
	return &Renderer{ctx: ctx}
}

func (r *Renderer) data() map[string]any {
	dbs := map[string]map[string]string{}
	for name, c := range r.ctx.Databases {
		dbs[name] = map[string]string{
			"host": c.Host, "port": c.Port, "user": c.User,
			"password": c.Password, "name": c.Name, "uri": c.URI,
		}
	}
	apps := map[string]map[string]string{}
	for name, alias := range r.ctx.Apps {
		apps[name] = map[string]string{"alias": alias}
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

// refPattern matches a dotted reference into one of the resolvable collections, e.g.
// ".databases.shop-db.uri" or ".secrets.app-key", capturing the collection and its trailing key segments.
// actionPattern bounds the rewrite to inside "{{ … }}" so literal text is never touched.
var (
	refPattern    = regexp.MustCompile(`\.(databases|secrets|applications|inputs)((?:\.[A-Za-z0-9_-]+)+)`)
	actionPattern = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
)

// expandRefs rewrites dotted collection accessors into calls to the strict `ref` function. That
// is what lets a reference name contain a hyphen: Go templates cannot access ".databases.shop-db"
// but `(ref "databases" "shop-db" "uri")` resolves it. Non-hyphenated refs rewrite identically.
func expandRefs(s string) string {
	return actionPattern.ReplaceAllStringFunc(s, func(action string) string {
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

// ref resolves a reference against the context, erroring on any unknown name or field so a typo
// (or a not-yet-provisioned dependency) fails loudly rather than rendering an empty value. It
// backs the rewritten "{{ .databases.x.y }}" syntax — see expandRefs.
func (r *Renderer) ref(coll string, keys ...string) (string, error) {
	if len(keys) == 0 {
		return "", fmt.Errorf("reference .%s needs a name", coll)
	}
	name := keys[0]
	field := ""
	if len(keys) > 1 {
		field = keys[1]
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
		fields := map[string]string{
			"host": c.Host, "port": c.Port, "user": c.User,
			"password": c.Password, "name": c.Name, "uri": c.URI,
		}
		if field == "" {
			field = "uri" // the most common reference; ".databases.x" alone means its URI
		}
		v, ok := fields[field]
		if !ok {
			return "", fmt.Errorf("database %q has no field %q", name, field)
		}
		return v, nil
	case "applications":
		alias, ok := r.ctx.Apps[name]
		if !ok {
			return "", fmt.Errorf("unknown application %q", name)
		}
		if field != "" && field != "alias" {
			return "", fmt.Errorf("application %q has no field %q", name, field)
		}
		return alias, nil
	}
	return "", fmt.Errorf("unknown reference collection %q", coll)
}

// funcs returns the template functions for one render: the static helpers plus
// the context-bound `ref`.
func (r *Renderer) funcs() template.FuncMap {
	fm := make(template.FuncMap, len(funcMap)+1)
	for k, v := range funcMap {
		fm[k] = v
	}
	fm["ref"] = r.ref
	return fm
}

// RenderString interpolates a single value. A reference to a missing key is a
// hard error (no silent empty env).
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

// RenderEnv interpolates every value of an env map.
func (r *Renderer) RenderEnv(scope string, env map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(env))
	for k, v := range env {
		rv, err := r.RenderString(scope+".env."+k, v)
		if err != nil {
			return nil, err
		}
		out[k] = rv
	}
	return out, nil
}

// RenderEnvLenient interpolates every value best-effort: a value whose references cannot resolve
// yet is left as its original template instead of failing. Used for plan/diff, where a
// not-yet-created dependency must not abort the plan; apply re-renders strictly.
func (r *Renderer) RenderEnvLenient(scope string, env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		if rv, err := r.RenderString(scope+".env."+k, v); err == nil {
			out[k] = rv
		} else {
			out[k] = v
		}
	}
	return out
}

const alphanum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// RandAlphaNum returns a cryptographically-random alphanumeric string of length
// n (used to satisfy secrets with generate: true).
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
