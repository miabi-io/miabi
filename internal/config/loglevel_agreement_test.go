// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config_test

import (
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/pkg/stack"
)

func TestInstallerAndControlPlaneAgreeOnLogLevels(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "warning", "error"} {
		m := stack.Defaults("miabi/miabi:1.4.0")
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

	for _, lvl := range []string{"off", "none", "silent", "verbose", "trace"} {
		if _, err := (&config.Config{LogLevel: lvl}).LogLevelFor(); err == nil {
			continue
		}
		m := stack.Defaults("miabi/miabi:1.4.0")
		m.Domain = "miabi.example.com"
		m.Env = map[string]string{"MIABI_LOG_LEVEL": lvl}
		if err := m.Normalize(); err == nil {
			t.Errorf("the installer accepts %q but the control plane rejects it — "+
				"the install would report success and the panel would never come up", lvl)
		}
	}
}
