// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import "testing"

func TestLabelValue(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		key    string
		want   string
		wantOK bool
	}{
		{"present", map[string]string{LabelApp: "42"}, LabelApp, "42", true},
		{"missing", map[string]string{"other": "x"}, LabelApp, "", false},
		{"nil map", nil, LabelApp, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := LabelValue(c.labels, c.key)
			if got != c.want || ok != c.wantOK {
				t.Errorf("LabelValue(%v,%q) = (%q,%v), want (%q,%v)", c.labels, c.key, got, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestIsManaged(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"platform label", map[string]string{LabelApp: "1"}, true},
		{"managed flag", map[string]string{LabelManaged: "true"}, true},
		{"unmanaged", map[string]string{"com.example": "x"}, false},
		{"empty", map[string]string{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsManaged(c.labels); got != c.want {
				t.Errorf("IsManaged(%v) = %v, want %v", c.labels, got, c.want)
			}
		})
	}
}

func TestWorkspaceID(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   uint
		wantOK bool
	}{
		{"present", map[string]string{LabelWorkspace: "5"}, 5, true},
		{"none", map[string]string{LabelApp: "1"}, 0, false},
		{"zero invalid", map[string]string{LabelWorkspace: "0"}, 0, false},
		{"non-numeric invalid", map[string]string{LabelWorkspace: "abc"}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := WorkspaceID(c.labels)
			if got != c.want || ok != c.wantOK {
				t.Errorf("WorkspaceID(%v) = (%d,%v), want (%d,%v)", c.labels, got, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestIsPlatformInfra(t *testing.T) {
	if !IsPlatformInfra(map[string]string{LabelRole: "node-gateway"}) {
		t.Error("role label should be infra")
	}
	if IsPlatformInfra(map[string]string{LabelApp: "1"}) {
		t.Error("app container is not infra")
	}
}

// --- the platform stack ------------------------------------------------------

// A component must never end up half-labeled: discoverable but not protected, or
// protected but not discoverable. PlatformLabels is the single constructor, so
// this pins all four keys at once.
func TestPlatformLabelsSetsTheWholeContract(t *testing.T) {
	l := PlatformLabels(RolePlatformDB, ManagedByCompose, nil)

	if !IsPlatformStack(l) {
		t.Error("not discoverable as platform stack")
	}
	if !IsProtected(l) {
		t.Error("not protected — an admin could stop the platform database")
	}
	if !IsManaged(l) {
		t.Error("not managed — housekeeping could treat it as a blanket prune target")
	}
	if !IsPlatformInfra(l) {
		t.Error("not infra — housekeeping could reclaim it as an orphan")
	}
	if got := ManagedBy(l); got != ManagedByCompose {
		t.Errorf("ManagedBy = %q, want %q", got, ManagedByCompose)
	}
	if got := l[LabelRole]; got != RolePlatformDB {
		t.Errorf("role = %q, want %q", got, RolePlatformDB)
	}
}

// extra may add component-specific keys but must never silently override the
// contract — a caller passing io.miabi.protected=false would otherwise disarm the
// destructive-op guard from the outside.
func TestPlatformLabelsExtraCannotOverrideTheContract(t *testing.T) {
	l := PlatformLabels(RoleNodeGateway, ManagedByMiabi, map[string]string{
		LabelNode:      "node-1",
		LabelProtected: "false", // hostile / mistaken
		LabelPartOf:    "not-miabi",
	})
	if !IsProtected(l) {
		t.Error("extra overrode protected — the guard can be disarmed by a caller")
	}
	if !IsPlatformStack(l) {
		t.Error("extra overrode part-of")
	}
	if l[LabelNode] != "node-1" {
		t.Error("extra dropped a legitimate component key")
	}
}

func TestIsPlatformStackAndIsProtected(t *testing.T) {
	cases := []struct {
		name             string
		labels           map[string]string
		stack, protected bool
	}{
		{"platform db", PlatformLabels(RolePlatformDB, ManagedByCompose, nil), true, true},
		{"user app", map[string]string{LabelApp: "1", LabelWorkspace: "2"}, false, false},
		{"raw container", map[string]string{}, false, false},
		{"nil labels", nil, false, false},
		// The registry GC collector is platform infra but transient: it must stay
		// removable, so it deliberately does not go through PlatformLabels.
		{"registry gc stays removable", map[string]string{LabelRole: RoleRegistryGC}, false, false},
		{"protected only counts when true", map[string]string{LabelProtected: "false"}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsPlatformStack(c.labels); got != c.stack {
				t.Errorf("IsPlatformStack = %v, want %v", got, c.stack)
			}
			if got := IsProtected(c.labels); got != c.protected {
				t.Errorf("IsProtected = %v, want %v", got, c.protected)
			}
		})
	}
}

// A user must not be able to hand-write these on their own container: a spoofed
// io.miabi.protected=true would make their app undeletable, and a spoofed
// part-of=miabi would hide it from housekeeping.
func TestPlatformStackLabelsAreReservedFromUsers(t *testing.T) {
	for _, k := range []string{LabelPartOf, LabelManagedBy, LabelProtected} {
		if !IsReservedLabelKey(k) {
			t.Errorf("%s is not reserved — a user could spoof it", k)
		}
	}
	clean := SanitizeUserLabels(map[string]string{
		LabelProtected: "true",
		LabelPartOf:    PartOfMiabi,
		"app.tier":     "web",
	})
	if IsProtected(clean) || IsPlatformStack(clean) {
		t.Errorf("user labels survived sanitization: %v", clean)
	}
	if clean["app.tier"] != "web" {
		t.Error("sanitization dropped a legitimate user label")
	}
}
