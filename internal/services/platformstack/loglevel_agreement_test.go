// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformstack_test

import (
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/services/platformstack"
)

// The installer validates MIABI_LOG_LEVEL so a typo fails instantly instead of
// crash-looping the control plane. That is only true while the installer's idea of a
// valid level matches the control plane's — two lists in two packages, and nothing
// but this test stops them drifting.
//
// It lives in a _test package so platformstack itself does not take a dependency on
// config (which drags in Okapi, GORM and the rest) just to share four strings.
func TestInstallerAndControlPlaneAgreeOnLogLevels(t *testing.T) {
	// Everything the installer accepts, the control plane must accept.
	for _, lvl := range []string{"debug", "info", "warn", "warning", "error"} {
		m := platformstack.Defaults("miabi/miabi:1.4.0")
		m.Domain = "miabi.example.com"
		m.Env = map[string]string{"MIABI_LOG_LEVEL": lvl}
		if err := m.Normalize(); err != nil {
			t.Errorf("the installer rejects %q: %v", lvl, err)
			continue
		}
		if _, err := (&config.Config{LogLevel: lvl}).LogLevelFor(); err != nil {
			t.Errorf("the installer accepts %q but the control plane rejects it (%v) — "+
				"the install would succeed and then crash-loop", lvl, err)
		}
	}

	// And what the control plane refuses, the installer must refuse too. "off" is the
	// live case: the logging library cannot honour it, so config rejects it — the
	// installer must not wave it through.
	for _, lvl := range []string{"off", "none", "silent", "verbose", "trace"} {
		if _, err := (&config.Config{LogLevel: lvl}).LogLevelFor(); err == nil {
			continue // the control plane accepts it, nothing to enforce here
		}
		m := platformstack.Defaults("miabi/miabi:1.4.0")
		m.Domain = "miabi.example.com"
		m.Env = map[string]string{"MIABI_LOG_LEVEL": lvl}
		if err := m.Normalize(); err == nil {
			t.Errorf("the installer accepts %q but the control plane rejects it — "+
				"the install would report success and the panel would never come up", lvl)
		}
	}
}
