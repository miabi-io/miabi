// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"text/template"
)

// ConnView is the connection detail of a resolved database, exposed to the
// renderer as {{ .databases.<name>.* }}.
type ConnView struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Name     string `json:"name"`
	URI      string `json:"uri"`
}

// Context is the read-only data available to env interpolation.
type Context struct {
	Inputs       map[string]string   // user answers (generated secrets already filled)
	Databases    map[string]ConnView // by database name
	Applications map[string]AppView  // by application name
}

// AppView exposes a sibling application's stable network alias.
type AppView struct {
	Alias string `json:"alias"`
}

// Renderer interpolates manifest strings against a Context.
type Renderer struct {
	ctx Context
}

// NewRenderer builds a renderer over ctx (nil maps are tolerated).
func NewRenderer(ctx Context) *Renderer {
	if ctx.Inputs == nil {
		ctx.Inputs = map[string]string{}
	}
	if ctx.Databases == nil {
		ctx.Databases = map[string]ConnView{}
	}
	if ctx.Applications == nil {
		ctx.Applications = map[string]AppView{}
	}
	return &Renderer{ctx: ctx}
}

// data shapes the context as nested maps so template paths like
// .databases.db.password resolve.
func (r *Renderer) data() map[string]any {
	dbs := map[string]map[string]string{}
	for name, c := range r.ctx.Databases {
		dbs[name] = map[string]string{
			"host": c.Host, "port": c.Port, "user": c.User,
			"password": c.Password, "name": c.Name, "uri": c.URI,
			"url": c.URI,
		}
	}
	apps := map[string]map[string]string{}
	for name, a := range r.ctx.Applications {
		apps[name] = map[string]string{"alias": a.Alias}
	}
	return map[string]any{
		"inputs":       r.ctx.Inputs,
		"databases":    dbs,
		"applications": apps,
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

// RenderString interpolates a single value. A reference to a missing key is a
// hard error (no silent empty env).
func (r *Renderer) RenderString(name, s string) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil // fast path: no interpolation
	}
	t, err := template.New(name).Funcs(funcMap).Option("missingkey=error").Parse(s)
	if err != nil {
		return "", fmt.Errorf("template %s: %w", name, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, r.data()); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return b.String(), nil
}

// RenderEnv interpolates every value of an application's env map.
func (r *Renderer) RenderEnv(app string, env map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(env))
	for k, v := range env {
		rv, err := r.RenderString(app+".env."+k, v)
		if err != nil {
			return nil, err
		}
		out[k] = rv
	}
	return out, nil
}

const alphanum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// RandAlphaNum returns a cryptographically-random alphanumeric string of length
// n (used to satisfy inputs with generate: true).
func RandAlphaNum(n int) string {
	if n <= 0 {
		n = 16
	}
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphanum)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand should never fail; fall back deterministically rather
			// than panic inside a render.
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
