// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
)

// baseDomain is a settings reader returning a fixed external base domain.
type baseDomain string

func (b baseDomain) String(_, def string) string {
	if b == "" {
		return def
	}
	return string(b)
}

// MIABI_REGISTRY_ENABLED pins enablement when it is set, and only then. An
// absent variable leaves the switch to the stored row — i.e. to the admin UI —
// so an install that configures nothing in the environment is driven entirely
// from the console.
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
			// An install upgraded from when the host was UI-editable must keep
			// answering on the name its existing images already reference.
			"stored value survives when env is unset",
			"", "registry.old.test", "example.com", "registry.old.test", "stored",
		},
		{"base domain when nothing is set", "", "", "example.com", "registry.example.com", "base_domain"},
		{"nothing at all", "", "", "", "", "unset"},
		// A host that matches no image reference must resolve to "" — distribution
		// then reports itself unavailable instead of running with an inert check.
		{"invalid env host is dropped", "https://registry.env.test", "", "example.com", "registry.example.com", "base_domain"},
		{"invalid stored host is dropped", "", "reg istry", "example.com", "registry.example.com", "base_domain"},
		{"invalid base domain yields no host", "", "", "not a domain", "", "unset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{cfg: config.RegistryConfig{Host: tc.envHost}, settings: baseDomain(tc.base)}
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

// A field the environment pins can never be written from the API: the value the
// operator declared must survive whatever the row holds, or a compose file would
// stop describing the install it deploys.
func TestEnvPinnedFieldsBeatStoredValues(t *testing.T) {
	svc := &Service{
		cfg:      config.RegistryConfig{Enabled: false, EnabledSet: true, Host: "registry.env.test", StorageType: "filesystem"},
		settings: baseDomain(""),
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
	svc := &Service{cfg: config.RegistryConfig{}, settings: baseDomain("")}
	if locks := svc.Locks(); locks.Any() {
		t.Errorf("Locks = %+v, want nothing pinned", locks)
	}
}
