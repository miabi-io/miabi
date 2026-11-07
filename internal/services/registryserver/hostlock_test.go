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

package registryserver

import (
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
)

type baseDomain string

func (b baseDomain) ExternalAccess(uint) (string, string) { return string(b), "" }

// An unset MIABI_REGISTRY_ENABLED leaves the switch to the admin UI.
func TestEnablementFollowsEnvOnlyWhenSet(t *testing.T) {
	cases := []struct {
		name   string
		env    bool
		envSet bool
		stored bool
		want   bool
	}{
		{"env on", true, true, false, true},
		{"env off beats a stored on", false, true, true, false},
		{"env on beats a stored off", true, true, false, true},
		{"unset: the stored value decides", false, false, true, true},
		{"unset and never enabled", false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &models.RegistrySettings{Enabled: tc.stored}
			svc := &Service{cfg: config.RegistryConfig{Enabled: tc.env, EnabledSet: tc.envSet}}
			svc.applyEnvConfig(st)
			if st.Enabled != tc.want {
				t.Errorf("Enabled = %v, want %v", st.Enabled, tc.want)
			}
			if got := svc.Locks().Enabled; got != tc.envSet {
				t.Errorf("Locks().Enabled = %v, want %v", got, tc.envSet)
			}
		})
	}
}

func TestHostResolution(t *testing.T) {
	cases := []struct {
		name       string
		envHost    string
		storedHost string
		base       string
		wantHost   string
		wantSource string
	}{
		{"env wins", "registry.env.test", "registry.old.test", "example.com", "registry.env.test", "env"},
		{
			// Upgraded installs keep answering on the host their existing images reference.
			"stored value survives when env is unset",
			"", "registry.old.test", "example.com", "registry.old.test", "stored",
		},
		{"base domain when nothing is set", "", "", "example.com", "registry.example.com", "base_domain"},
		{"nothing at all", "", "", "", "", "unset"},
		// An invalid host resolves to "" so distribution reports itself unavailable.
		{"invalid env host is dropped", "https://registry.env.test", "", "example.com", "registry.example.com", "base_domain"},
		{"invalid stored host is dropped", "", "reg istry", "example.com", "registry.example.com", "base_domain"},
		{"invalid base domain yields no host", "", "", "not a domain", "", "unset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{cfg: config.RegistryConfig{Host: tc.envHost}, external: baseDomain(tc.base)}
			st := &models.RegistrySettings{Host: tc.storedHost}
			svc.applyEnvConfig(st)
			if got := svc.HostFor(st); got != tc.wantHost {
				t.Errorf("HostFor = %q, want %q", got, tc.wantHost)
			}
			if got := svc.HostSource(st); got != tc.wantSource {
				t.Errorf("HostSource = %q, want %q", got, tc.wantSource)
			}
		})
	}
}

// Env-pinned fields can never be written from the API, or the compose file would stop describing the install.
func TestEnvPinnedFieldsBeatStoredValues(t *testing.T) {
	svc := &Service{
		cfg:      config.RegistryConfig{Enabled: false, EnabledSet: true, Host: "registry.env.test", StorageType: "filesystem"},
		external: baseDomain(""),
	}
	st := &models.RegistrySettings{Enabled: true, Host: "registry.stale.test", StorageType: models.RegistryStorageS3}
	svc.applyEnvConfig(st)

	if st.Enabled {
		t.Error("a stored enabled=true survived an env that pins it off")
	}
	if st.Host != "registry.env.test" {
		t.Errorf("Host = %q, want the env value to win", st.Host)
	}
	if st.StorageType != models.RegistryStorageFilesystem {
		t.Errorf("StorageType = %q, want the env value to win", st.StorageType)
	}
	locks := svc.Locks()
	if !locks.Enabled || !locks.Host || !locks.Storage {
		t.Errorf("Locks = %+v, want enablement, host and storage all pinned", locks)
	}
}

// With nothing in the environment, every field is the console's to set.
func TestNothingIsLockedWithoutEnv(t *testing.T) {
	svc := &Service{cfg: config.RegistryConfig{}, external: baseDomain("")}
	if locks := svc.Locks(); locks.Any() {
		t.Errorf("Locks = %+v, want nothing pinned", locks)
	}
}
