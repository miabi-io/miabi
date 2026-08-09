// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

const configYAML = `
apiVersion: miabi.io/v1
kind: Config
metadata: { name: prom-conf }
spec:
  data:
    prometheus.yml: |
      global: { scrape_interval: 15s }
    rules/alerts.yml: "groups: []"
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: prometheus }
spec:
  image: prom/prometheus
  tag: v3.1.0
  mounts:
    - config: prom-conf
      path: /etc/prometheus
    - config: prom-conf
      key: rules/alerts.yml
      path: /etc/prometheus/rules/alerts.yml
      mode: "0444"
`

func parseConfigSet(t *testing.T, y string) *d.ResourceSet {
	t.Helper()
	set, err := d.Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return set
}

func TestParseConfig(t *testing.T) {
	set := parseConfigSet(t, configYAML)
	r, ok := set.Get(string(d.KindConfig) + "/prom-conf")
	if !ok || r.Config == nil {
		t.Fatal("config not parsed")
	}
	if len(r.Config.Data) != 2 {
		t.Fatalf("data = %d keys, want 2", len(r.Config.Data))
	}
	if r.Config.Mode != d.DefaultFileMode {
		t.Errorf("mode = %q, want the %q default", r.Config.Mode, d.DefaultFileMode)
	}
}

// A config mount is always projected read-only: an explicit readOnly: false is
// normalized away rather than rejected, so the guarantee holds either way.
func TestConfigMountIsAlwaysReadOnly(t *testing.T) {
	set := parseConfigSet(t, configYAML)
	app, _ := set.Get(string(d.KindApplication) + "/prometheus")
	for _, mt := range app.Application.Mounts {
		if !mt.ReadOnly {
			t.Errorf("mount %q is not read-only", mt.Path)
		}
	}

	y := "apiVersion: miabi.io/v1\nkind: Config\nmetadata: { name: c }\nspec: { data: { a.txt: x } }\n---\napiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: app }\nspec:\n  image: nginx\n  mounts: [{ config: c, path: /x, readOnly: false }]\n"
	forced, err := d.Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	a, _ := forced.Get(string(d.KindApplication) + "/app")
	if !a.Application.Mounts[0].ReadOnly {
		t.Error("explicit readOnly: false was not normalized to true")
	}
}

func TestParseRejectsUnknownConfigField(t *testing.T) {
	y := `
apiVersion: miabi.io/v1
kind: Config
metadata: { name: c }
spec: { data: { a.txt: x }, nope: 1 }
`
	if _, err := d.Parse([]byte(y)); err == nil {
		t.Fatal("expected an unknown-field error")
	}
}

func TestConfigKeyRules(t *testing.T) {
	cases := map[string]bool{
		"prometheus.yml":   true,
		"rules/alerts.yml": true,
		"a_b-c.conf":       true,
		"../etc/passwd":    false,
		"/etc/passwd":      false,
		"a//b":             false,
		"a/":               false,
		"":                 false,
	}
	for key, want := range cases {
		y := "apiVersion: miabi.io/v1\nkind: Config\nmetadata: { name: c }\nspec:\n  data:\n    \"" + key + "\": x\n"
		_, err := d.Parse([]byte(y))
		if want && err != nil {
			t.Errorf("key %q: unexpected error: %v", key, err)
		}
		if !want && err == nil {
			t.Errorf("key %q: expected rejection", key)
		}
	}
}

func TestConfigSizeCaps(t *testing.T) {
	within := strings.Repeat("x", d.MaxConfigFileBytes)
	over := strings.Repeat("x", d.MaxConfigFileBytes+1)

	y := "apiVersion: miabi.io/v1\nkind: Config\nmetadata: { name: c }\nspec:\n  data:\n    a.txt: " + within + "\n"
	if _, err := d.Parse([]byte(y)); err != nil {
		t.Errorf("file at the cap should pass: %v", err)
	}
	y = "apiVersion: miabi.io/v1\nkind: Config\nmetadata: { name: c }\nspec:\n  data:\n    a.txt: " + over + "\n"
	if _, err := d.Parse([]byte(y)); err == nil {
		t.Error("file over the cap should fail")
	}

	// Three at-cap files stay under the per-file limit but blow the total.
	y = "apiVersion: miabi.io/v1\nkind: Config\nmetadata: { name: c }\nspec:\n  data:\n"
	for _, k := range []string{"a.txt", "b.txt", "c.txt"} {
		y += "    " + k + ": " + within + "\n"
	}
	if _, err := d.Parse([]byte(y)); err == nil {
		t.Error("total over the cap should fail")
	}
}

func TestConfigModeRules(t *testing.T) {
	for _, mode := range []string{"0644", "644", "0444"} {
		y := "apiVersion: miabi.io/v1\nkind: Config\nmetadata: { name: c }\nspec: { mode: \"" + mode + "\", data: { a.txt: x } }\n"
		if _, err := d.Parse([]byte(y)); err != nil {
			t.Errorf("mode %q: %v", mode, err)
		}
	}
	for _, mode := range []string{"04755", "4755", "2755", "1755", "0999", "64"} {
		y := "apiVersion: miabi.io/v1\nkind: Config\nmetadata: { name: c }\nspec: { mode: \"" + mode + "\", data: { a.txt: x } }\n"
		if _, err := d.Parse([]byte(y)); err == nil {
			t.Errorf("mode %q should be rejected", mode)
		}
	}
}

func TestConfigDelimiterRules(t *testing.T) {
	ok := `apiVersion: miabi.io/v1
kind: Config
metadata: { name: c }
spec: { delimiters: ["<<", ">>"], data: { a.txt: x } }
`
	if _, err := d.Parse([]byte(ok)); err != nil {
		t.Errorf("valid delimiters: %v", err)
	}
	for _, delims := range []string{`["<<"]`, `["<<", ">>", "!!"]`, `["", ">>"]`, `["<<", "<<"]`} {
		y := "apiVersion: miabi.io/v1\nkind: Config\nmetadata: { name: c }\nspec: { delimiters: " + delims + ", data: { a.txt: x } }\n"
		if _, err := d.Parse([]byte(y)); err == nil {
			t.Errorf("delimiters %s should be rejected", delims)
		}
	}
}

func TestMountSourceExclusivity(t *testing.T) {
	base := "apiVersion: miabi.io/v1\nkind: Config\nmetadata: { name: c }\nspec: { data: { a.txt: x } }\n---\napiVersion: miabi.io/v1\nkind: Volume\nmetadata: { name: v }\nspec: {}\n---\napiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: app }\nspec:\n  image: nginx\n  mounts: "

	for _, tc := range []struct {
		name   string
		mounts string
	}{
		{"neither source", `[{ path: /x }]`},
		{"both sources", `[{ volume: v, config: c, path: /x }]`},
		{"key without config", `[{ volume: v, key: a.txt, path: /x }]`},
		{"mode without config", `[{ volume: v, mode: "0444", path: /x }]`},
		{"relative path", `[{ config: c, path: etc }]`},
		{"key with directory path", `[{ config: c, key: a.txt, path: /etc/ }]`},
		{"unknown config", `[{ config: nope, path: /x }]`},
		{"mount nested inside a volume", `[{ volume: v, path: /data }, { config: c, path: /data/app.conf }]`},
	} {
		if _, err := d.Parse([]byte(base + tc.mounts + "\n")); err == nil {
			t.Errorf("%s: expected rejection", tc.name)
		}
	}
}

func TestConfigEdgeAndOrdering(t *testing.T) {
	set := parseConfigSet(t, configYAML)

	var found bool
	for _, e := range d.Edges(set) {
		if e.Type == d.EdgeConfig && e.To == string(d.KindConfig)+"/prom-conf" && e.From == string(d.KindApplication)+"/prometheus" {
			found = true
		}
	}
	if !found {
		t.Error("no EdgeConfig from the application to the config")
	}

	plan := d.BuildPlan(set, d.NewResourceSet(), d.PlanOptions{})
	cfgAt, appAt := -1, -1
	for i, c := range plan.Changes {
		switch c.Kind {
		case d.KindConfig:
			cfgAt = i
		case d.KindApplication:
			appAt = i
		}
	}
	if cfgAt < 0 || appAt < 0 || cfgAt > appAt {
		t.Errorf("config must be planned before the application (config=%d app=%d)", cfgAt, appAt)
	}
}

func TestSensitiveConfigDiffHidesContent(t *testing.T) {
	actual := d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindConfig,
		Metadata: d.Meta{Name: "c"},
		Config:   &d.ConfigSpec{Data: map[string]string{"a.txt": "old-secret"}, Sensitive: true, DigestFP: "aaa", Mode: d.DefaultFileMode},
	}
	desired := d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindConfig,
		Metadata: d.Meta{Name: "c"},
		Config:   &d.ConfigSpec{Data: map[string]string{"a.txt": "new-secret"}, Sensitive: true, DigestFP: "bbb", Mode: d.DefaultFileMode},
	}
	act, des := d.NewResourceSet(), d.NewResourceSet()
	act.Add(actual)
	des.Add(desired)
	plan := d.BuildPlan(des, act, d.PlanOptions{})
	if len(plan.Changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(plan.Changes))
	}
	for _, f := range plan.Changes[0].Fields {
		if strings.Contains(f.From, "secret") || strings.Contains(f.To, "secret") {
			t.Fatalf("content leaked into the plan: %+v", f)
		}
	}
}

func TestConfigMarshalRoundTrip(t *testing.T) {
	set := parseConfigSet(t, configYAML)
	r, _ := set.Get(string(d.KindConfig) + "/prom-conf")
	r.Config.DigestFP = "must-not-serialize"
	one := d.NewResourceSet()
	one.Add(r)

	out, err := d.Marshal(one)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "must-not-serialize") {
		t.Fatal("DigestFP was serialized")
	}
	back, err := d.Parse(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	rt, _ := back.Get(string(d.KindConfig) + "/prom-conf")
	if rt.Config.DigestFP != "" {
		t.Errorf("DigestFP survived a round trip: %q", rt.Config.DigestFP)
	}
	again, err := d.Marshal(back)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(out) != string(again) {
		t.Errorf("round trip is not byte-stable:\n%s\n---\n%s", out, again)
	}
}
