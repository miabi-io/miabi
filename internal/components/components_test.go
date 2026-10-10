// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package components

import (
	"regexp"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/config"
)

func byID(list []Component) map[string]Component {
	m := map[string]Component{}
	for _, c := range list {
		m[c.ID] = c
	}
	return m
}

func TestListOrderAndVersions(t *testing.T) {
	list := NewService(Probes{}).List()
	want := []string{"web", "gitops", "pipeline", "marketplace", "analytics", "registry", "storage-classes", "healthprobe", "sealed-secrets"}
	if len(list) != len(want) {
		t.Fatalf("got %d components, want %d", len(list), len(want))
	}
	semver := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	for i, c := range list {
		if c.ID != want[i] {
			t.Errorf("component %d = %s, want %s", i, c.ID, want[i])
		}
		if c.ID != "web" && !semver.MatchString(c.Version) {
			t.Errorf("%s version %q is not semver", c.ID, c.Version)
		}
		if c.ChangelogURL == "" {
			t.Errorf("%s has no changelog link", c.ID)
		}
	}
	if byID(list)["web"].Version != config.Version {
		t.Error("the Web UI must carry Miabi's own version")
	}
}

func TestStatusFollowsProbes(t *testing.T) {
	off := func() bool { return false }
	c := byID(NewService(Probes{RegistryEnabled: off, AnalyticsEnabled: off, HealthProbeBundled: off}).List())
	if c["healthprobe"].Status != StatusOff {
		t.Errorf("healthprobe = %s, want off when the build lacks the binaries", c["healthprobe"].Status)
	}
	if c["analytics"].Status != StatusOff {
		t.Errorf("analytics = %s, want off", c["analytics"].Status)
	}
	if c["registry"].Status != StatusOff {
		t.Errorf("registry = %s, want off", c["registry"].Status)
	}
	if c["storage-classes"].Status != StatusOn {
		t.Errorf("storage classes = %s, want on: Community can register classes too", c["storage-classes"].Status)
	}

	c = byID(NewService(Probes{}).List())
	if c["registry"].Status != StatusOn || c["storage-classes"].Status != StatusOn {
		t.Error("unwired probes must read as on")
	}
}

func TestMarketplaceCatalog(t *testing.T) {
	synced := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	c := byID(NewService(Probes{Catalog: func() Catalog {
		return Catalog{Templates: 58, SyncedAt: &synced}
	}}).List())
	cat := c["marketplace"].Catalog
	if cat == nil || cat.Templates != 58 || !cat.SyncedAt.Equal(synced) {
		t.Fatalf("catalog = %+v", cat)
	}
	if byID(NewService(Probes{}).List())["marketplace"].Catalog != nil {
		t.Error("no catalog probe must mean no catalog")
	}
}
