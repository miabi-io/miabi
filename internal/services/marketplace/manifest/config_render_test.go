// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"strings"
	"testing"
)

// A Prometheus rules file contains literal {{ $labels.instance }}, which is a
// hard render error under the default delimiters. Custom delimiters are what
// make such a file storable at all.
func TestRenderFilesHonoursDelimiters(t *testing.T) {
	r := NewRenderer(Context{
		Inputs:       map[string]string{"for_duration": "5m"},
		Applications: map[string]AppView{"api": {Alias: "mb-app-api"}},
	})

	files := map[string]string{
		"alerts.yml": "expr: up == 0\nfor: << .inputs.for_duration >>\nsummary: \"{{ $labels.instance }} is down\"\n",
	}
	out, err := r.RenderFiles("rules", files, []string{"<<", ">>"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := out["alerts.yml"]
	if !strings.Contains(got, "for: 5m") {
		t.Errorf("input was not interpolated: %q", got)
	}
	if !strings.Contains(got, "{{ $labels.instance }}") {
		t.Errorf("the file's own {{ }} was mangled: %q", got)
	}

	// The same file under default delimiters must fail rather than silently drop
	// the label expression.
	if _, err := r.RenderFiles("rules", files, nil); err == nil {
		t.Error("expected a render error for {{ $labels.instance }} under default delimiters")
	}
}

func TestRenderFilesResolvesAppAliasAndDatabases(t *testing.T) {
	r := NewRenderer(Context{
		Databases:    map[string]ConnView{"db": {Host: "mb-db-1", Port: "5432"}},
		Applications: map[string]AppView{"api": {Alias: "mb-app-api"}},
	})
	out, err := r.RenderFiles("conf", map[string]string{
		"prometheus.yml": "targets: [\"{{ .applications.api.alias }}:8080\", \"{{ .databases.db.host }}:{{ .databases.db.port }}\"]",
	}, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := "targets: [\"mb-app-api:8080\", \"mb-db-1:5432\"]"
	if out["prometheus.yml"] != want {
		t.Errorf("got %q, want %q", out["prometheus.yml"], want)
	}
}

func TestValidateConfigRules(t *testing.T) {
	base := func(c Config) *Manifest {
		return &Manifest{
			APIVersion: APIVersion, Kind: KindValue,
			Metadata: Metadata{Name: "t", DisplayName: "T", Version: "1.0.0"},
			Configs:  []Config{c},
			Applications: []AppSpec{{
				Name: "app", Image: "nginx",
				Mounts: []Mount{{Config: c.Name, Path: "/etc/app"}},
			}},
		}
	}
	if err := base(Config{Name: "conf", Files: map[string]string{"a.yml": "x"}}).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, c := range map[string]Config{
		"no files":       {Name: "conf"},
		"bad key":        {Name: "conf", Files: map[string]string{"../x": "y"}},
		"bad mode":       {Name: "conf", Files: map[string]string{"a": "b"}, Mode: "04755"},
		"one delimiter":  {Name: "conf", Files: map[string]string{"a": "b"}, Delimiters: []string{"<<"}},
		"same delimiter": {Name: "conf", Files: map[string]string{"a": "b"}, Delimiters: []string{"<<", "<<"}},
		"oversized file": {Name: "conf", Files: map[string]string{"a": strings.Repeat("x", MaxConfigFileBytes+1)}},
	} {
		if err := base(c).Validate(); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestValidateMountSourceExclusivity(t *testing.T) {
	m := &Manifest{
		APIVersion: APIVersion, Kind: KindValue,
		Metadata: Metadata{Name: "t", DisplayName: "T", Version: "1.0.0"},
		Configs:  []Config{{Name: "conf", Files: map[string]string{"a.yml": "x"}}},
		Volumes:  []Volume{{Name: "data"}},
	}
	for name, mt := range map[string]Mount{
		"neither":         {Path: "/x"},
		"both":            {Volume: "data", Config: "conf", Path: "/x"},
		"unknown config":  {Config: "nope", Path: "/x"},
		"key without cfg": {Volume: "data", Key: "a.yml", Path: "/x"},
	} {
		m.Applications = []AppSpec{{Name: "app", Image: "nginx", Mounts: []Mount{mt}}}
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}
