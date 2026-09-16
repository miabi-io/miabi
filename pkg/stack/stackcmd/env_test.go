// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stackcmd

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/pkg/stack"
)

// recUI records what the operator would have been shown, so the tests can assert on the explanation
// and not just the file.
type recUI struct{ lines []string }

func (u *recUI) Printf(f string, a ...any)  { u.lines = append(u.lines, sprintf(f, a...)) }
func (u *recUI) Info(f string, a ...any)    { u.lines = append(u.lines, sprintf(f, a...)) }
func (u *recUI) Success(f string, a ...any) { u.lines = append(u.lines, sprintf(f, a...)) }
func (u *recUI) Warn(f string, a ...any)    { u.lines = append(u.lines, sprintf(f, a...)) }
func (u *recUI) Confirm(string) bool        { return true }
func (u *recUI) said(sub string) bool       { return strings.Contains(strings.Join(u.lines, "\n"), sub) }

func sprintf(f string, a ...any) string {
	if len(a) == 0 {
		return f // keep a literal % intact
	}
	return strings.TrimRight(fmt.Sprintf(f, a...), "\n")
}

// installedManifest writes a converged manifest to a temp path and returns it.
func installedManifest(t *testing.T) string {
	t.Helper()
	m := stack.Defaults("miabi/miabi:1.10.0")
	m.Domain = "miabi.example.com"
	if err := m.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	path := filepath.Join(t.TempDir(), "miabi.yaml")
	if err := stack.Save(path, m); err != nil {
		t.Fatalf("save: %v", err)
	}
	return path
}

// NoApply returns before Converge, so these exercise the whole edit path without a Docker engine.
func batch() EnvOptions { return EnvOptions{NoApply: true, Yes: true} }

func TestEnvSetWritesTheVariable(t *testing.T) {
	path := installedManifest(t)
	ui := &recUI{}

	if err := EnvSet(context.Background(), nil, path, []string{"MIABI_SMTP_HOST=smtp.example.com"}, batch(), ui); err != nil {
		t.Fatal(err)
	}

	got, _, err := EnvList(path, EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got["MIABI_SMTP_HOST"] != "smtp.example.com" {
		t.Errorf("MIABI_SMTP_HOST = %q, want it written to the manifest", got["MIABI_SMTP_HOST"])
	}
	if !ui.said("MIABI_SMTP_HOST") {
		t.Error("the change was applied without showing the operator what changed")
	}
	if !ui.said("still has the old values") {
		t.Error("--no-apply did not warn that the file and the running stack now disagree")
	}
}

// Validation is Normalize's, and its errors name where the value really lives. That is the whole
// reason this verb does not re-implement any checking.
func TestEnvSetRefusesAKeyThatHasItsOwnHome(t *testing.T) {
	cases := map[string]struct{ assign, want string }{
		"a registry setting": {"MIABI_REGISTRY_HOST=registry.example.com", "registry"},
		"the gateway key":    {"GOMA_CONFIG_ENCRYPTION_KEY=abc", "spec.secrets"},
		"a managed value":    {"MIABI_DB_PASSWORD=hunter2", "set by Miabi itself"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := installedManifest(t)
			err := EnvSet(context.Background(), nil, path, []string{tc.assign}, batch(), &recUI{})
			if err == nil {
				t.Fatalf("accepted %q, which has its own home in the manifest", tc.assign)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to point at %q", err, tc.want)
			}
		})
	}
}

func TestEnvUnsetRemovesTheVariable(t *testing.T) {
	path := installedManifest(t)
	if err := EnvSet(context.Background(), nil, path, []string{"MIABI_SMTP_HOST=smtp.example.com"}, batch(), &recUI{}); err != nil {
		t.Fatal(err)
	}
	if err := EnvUnset(context.Background(), nil, path, []string{"MIABI_SMTP_HOST"}, batch(), &recUI{}); err != nil {
		t.Fatal(err)
	}

	if _, found, err := EnvGet(path, "MIABI_SMTP_HOST", EnvOptions{}); err != nil || found {
		t.Errorf("MIABI_SMTP_HOST is still set (found=%v, err=%v)", found, err)
	}
}

// Miabi seeds a couple of variables into the manifest, so unsetting one restores its default instead
// of removing it. Saying so beats letting the operator diff the file and find it still there.
func TestUnsettingASeededDefaultRestoresIt(t *testing.T) {
	path := installedManifest(t)
	ui := &recUI{}

	if err := EnvUnset(context.Background(), nil, path, []string{"TZ"}, batch(), ui); err != nil {
		t.Fatal(err)
	}

	got, _, err := EnvList(path, EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got["TZ"] != stack.DefaultTimezone {
		t.Errorf("TZ = %q, want it back at the default %q", got["TZ"], stack.DefaultTimezone)
	}
	if !ui.said("seeded by Miabi") {
		t.Error("the operator was not told the variable came back at its default")
	}
}

func TestEnvTargetsTheGatewayBlock(t *testing.T) {
	path := installedManifest(t)
	gw := EnvOptions{Gateway: true, NoApply: true, Yes: true}

	if err := EnvSet(context.Background(), nil, path, []string{"MY_UPSTREAM=https://internal.example.com"}, gw, &recUI{}); err != nil {
		t.Fatal(err)
	}

	gatewayEnv, block, err := EnvList(path, EnvOptions{Gateway: true})
	if err != nil {
		t.Fatal(err)
	}
	if gatewayEnv["MY_UPSTREAM"] == "" {
		t.Error("the value did not reach the gateway block")
	}
	if block != "spec.gateway.env" {
		t.Errorf("block = %q, want spec.gateway.env", block)
	}

	serverEnv, _, err := EnvList(path, EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := serverEnv["MY_UPSTREAM"]; leaked {
		t.Error("a gateway variable was written into the control plane's environment as well")
	}
}

func TestDiffEnvReportsAdditionsRemovalsAndChanges(t *testing.T) {
	before := map[string]string{"KEEP": "1", "CHANGE": "old", "GONE": "x"}
	after := map[string]string{"KEEP": "1", "CHANGE": "new", "ADDED": "y"}

	want := []envChange{
		{key: "ADDED", from: "", to: "y"},
		{key: "CHANGE", from: "old", to: "new"},
		{key: "GONE", from: "x", to: ""},
	}
	if got := diffEnv(before, after); !reflect.DeepEqual(got, want) {
		t.Errorf("diffEnv = %+v, want %+v", got, want)
	}
}
