// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stackcmd

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/pkg/stack"
)

func manifestFor(web, control string) *stack.Manifest {
	m := &stack.Manifest{Domain: "example.com", WebURL: web, ControlURL: control}
	m.Secrets.AdminEmail = "admin@example.com"
	m.Secrets.AdminPassword = "p@ssw0rd-with-an-at-sign"
	return m
}

// Two bare lines left the reader to work out which was which — and a password containing an "@"
// makes that guess wrong, not merely slow.
func TestPrintResultLabelsTheCredentials(t *testing.T) {
	ui := &recUI{}
	printResult(ui, manifestFor("https://example.com", ""), "/etc/miabi/miabi.yaml", true)

	for _, want := range []string{
		"Email      admin@example.com",
		"Password   p@ssw0rd-with-an-at-sign",
	} {
		if !ui.said(want) {
			t.Errorf("summary is missing %q\n%s", want, strings.Join(ui.lines, "\n"))
		}
	}
}

// An existing install is not shown a password it was not issued.
func TestPrintResultOmitsCredentialsOnUpgrade(t *testing.T) {
	ui := &recUI{}
	printResult(ui, manifestFor("https://example.com", ""), "/etc/miabi/miabi.yaml", false)
	if ui.said("Password") {
		t.Errorf("an upgrade printed credentials\n%s", strings.Join(ui.lines, "\n"))
	}
}

// Agents and runners dial the control URL, which need not be the panel's. When it differs the
// operator has to be told; when it does not, repeating the same URL under a second name reads as a
// distinction they then go looking for.
func TestControlURLShownOnlyWhenItDiffers(t *testing.T) {
	same := &recUI{}
	printResult(same, manifestFor("https://example.com", "https://example.com"), "/p", true)
	if strings.Count(strings.Join(same.lines, "\n"), "https://example.com") > 1 {
		t.Errorf("the panel URL was printed twice under two names\n%s", strings.Join(same.lines, "\n"))
	}

	diff := &recUI{}
	printResult(diff, manifestFor("https://example.com", "https://control.internal:9000"), "/p", true)
	if !diff.said("https://control.internal:9000") {
		t.Errorf("a distinct control URL was not shown\n%s", strings.Join(diff.lines, "\n"))
	}

	// Empty means "defaults to the web URL", so it is not a second address either.
	empty := &recUI{}
	printResult(empty, manifestFor("https://example.com", ""), "/p", true)
	if empty.said("dial") || empty.said("reach it at") {
		t.Errorf("an unset control URL was announced\n%s", strings.Join(empty.lines, "\n"))
	}
}

// The plan an operator approves before anything runs shows it too.
func TestPrintPlanShowsADistinctControlURL(t *testing.T) {
	ui := &recUI{}
	m := manifestFor("https://example.com", "https://control.internal:9000")
	m.Images.Miabi, m.Images.Gateway = "miabi/miabi:1", "goma:1"
	m.Images.Postgres, m.Images.Redis = "pg:17", "redis:7"
	printPlan(ui, m, "/etc/miabi/miabi.yaml")

	if !ui.said("https://control.internal:9000") {
		t.Errorf("the plan hides where nodes dial back\n%s", strings.Join(ui.lines, "\n"))
	}
	same := &recUI{}
	m2 := manifestFor("https://example.com", "https://example.com")
	printPlan(same, m2, "/p")
	if same.said("agents and runners dial back") {
		t.Errorf("the plan announced a control URL identical to the panel's\n%s", strings.Join(same.lines, "\n"))
	}
}

// A separate control URL is usually an address only nodes can route to. The advice for it is the
// opposite of the domain's: it must resolve from the NODES, and it is not a Let's Encrypt name.
func TestControlHostAdvice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		web     string
		control string
		want    string
	}{
		{"a private name nodes dial", "https://example.com", "https://control.internal:9000", "control.internal:9000"},
		{"an IP", "https://example.com", "http://10.0.0.5:9000", "10.0.0.5:9000"},
		{"unset — defaults to the web URL", "https://example.com", "", ""},
		{"identical to the web URL", "https://example.com", "https://example.com", ""},
		{"same name, other port — DNS already covered", "https://example.com", "https://example.com:9000", ""},
		{"unparseable", "https://example.com", "://nonsense", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := controlHost(manifestFor(tc.web, tc.control)); got != tc.want {
				t.Errorf("controlHost = %q, want %q", got, tc.want)
			}
		})
	}
}

// The two DNS sentences must stay apart: pointing a private control address at a public IP, or
// expecting Let's Encrypt to validate it, is the opposite of what it is for.
func TestControlHostIsNotInTheLetsEncryptLine(t *testing.T) {
	ui := &recUI{}
	printResult(ui, manifestFor("https://example.com", "https://control.internal:9000"), "/p", true)
	out := strings.Join(ui.lines, "\n")

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "public IP") && strings.Contains(line, "control.internal") {
			t.Errorf("the control host was folded into the public-DNS advice:\n%s", line)
		}
	}
	if !strings.Contains(out, "resolve from your NODES") {
		t.Errorf("no node-side resolution advice given:\n%s", out)
	}
}
