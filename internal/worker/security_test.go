// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// chownFake records the images RunOneShot is invoked with and lets a test force the app-image chown
// to fail so the busybox fallback path is exercised. It embeds docker.Client so only the methods used
// here need implementing.
type chownFake struct {
	docker.Client
	oneShotImages []string
	pulled        []string
	failImage     string // RunOneShot returns exit 1 for this image (e.g. no chown binary)
}

func (f *chownFake) RunOneShot(_ context.Context, spec docker.RunSpec) (int, string, error) {
	f.oneShotImages = append(f.oneShotImages, spec.Image)
	if spec.Image == f.failImage {
		return 1, "chown: not found", nil
	}
	return 0, "", nil
}

func (f *chownFake) PullImage(_ context.Context, ref string, _ *docker.RegistryAuth) error {
	f.pulled = append(f.pulled, ref)
	return nil
}

func TestPrepareVolumeOwnership(t *testing.T) {
	b := &runtimeBuilder{securityInitImage: "busybox:latest"}
	restricted := Security{User: "100000:0", Restricted: true}
	mounts := map[string]string{"vol1": "/data"}

	// Not restricted: no chown container runs at all.
	f := &chownFake{}
	if err := b.prepareVolumeOwnership(context.Background(), f, Security{}, "wordpress:6", mounts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.oneShotImages) != 0 {
		t.Errorf("no-op expected for the default profile, ran %v", f.oneShotImages)
	}

	// Restricted, app image can chown: the app image seeds + chowns; busybox unused.
	f = &chownFake{}
	if err := b.prepareVolumeOwnership(context.Background(), f, restricted, "wordpress:6", mounts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.oneShotImages) != 1 || f.oneShotImages[0] != "wordpress:6" {
		t.Errorf("expected chown via the app image only, got %v", f.oneShotImages)
	}

	// Restricted, app image lacks chown: falls back to the busybox init image, which
	// corrects ownership of the volume the app image already seeded.
	f = &chownFake{failImage: "distroless:app"}
	if err := b.prepareVolumeOwnership(context.Background(), f, restricted, "distroless:app", mounts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.oneShotImages) != 2 || f.oneShotImages[0] != "distroless:app" || f.oneShotImages[1] != "busybox:latest" {
		t.Errorf("expected app image then busybox fallback, got %v", f.oneShotImages)
	}

	// A custom user under the DEFAULT profile still owns its volumes: the chown
	// follows the pinned user, not the profile.
	f = &chownFake{}
	if err := b.prepareVolumeOwnership(context.Background(), f, Security{User: "1000:1000"}, "app:1", mounts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.oneShotImages) != 1 {
		t.Errorf("a pinned user should own its volumes, ran %v", f.oneShotImages)
	}
}

func TestSecurityApplyTo(t *testing.T) {
	// Zero value = no restriction; RunSpec stays at image defaults.
	var spec docker.RunSpec
	(Security{}).applyTo(&spec)
	if spec.User != "" || spec.NoNewPrivileges || spec.CapDrop != nil {
		t.Errorf("zero Security should leave the spec untouched, got %+v", spec)
	}
	if (Security{}).HasUser() {
		t.Error("zero Security must not report a pinned user")
	}

	// Restricted profile stamps user + hardening.
	sec := Security{User: "100000:0", NoNewPrivileges: true, CapDrop: []string{"NET_RAW"}, Restricted: true}
	sec.applyTo(&spec)
	if !sec.HasUser() {
		t.Error("Security with a user should report one")
	}
	if spec.User != "100000:0" || !spec.NoNewPrivileges || len(spec.CapDrop) != 1 {
		t.Errorf("restricted Security not applied: %+v", spec)
	}
}

func TestWithRunAsUser(t *testing.T) {
	restricted := Security{User: "100000:0", NoNewPrivileges: true, CapDrop: []string{"NET_RAW"}, Restricted: true}

	// Default profile: any account the image understands, root included.
	for _, v := range []string{"1000", "1000:1000", "node", "node:node", "0"} {
		got, err := (Security{}).withRunAsUser(v)
		if err != nil {
			t.Errorf("%q should be allowed under the default profile: %v", v, err)
		}
		if got.User != v {
			t.Errorf("user = %q, want %q", got.User, v)
		}
	}

	// Restricted: a non-root numeric uid replaces the platform UID, and the rest of
	// the hardening contract survives.
	got, err := restricted.withRunAsUser("1000:1000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.User != "1000:1000" || !got.Restricted || !got.NoNewPrivileges || len(got.CapDrop) != 1 {
		t.Errorf("restricted hardening not preserved: %+v", got)
	}

	// Restricted: anything the platform can't verify as non-root is refused — root
	// outright, and names, which the image's own /etc/passwd is free to map to uid 0.
	for _, v := range []string{"0", "0:0", "root", "appuser", "1000:group"} {
		if _, err := restricted.withRunAsUser(v); !errors.Is(err, ErrRunAsUserForbidden) {
			t.Errorf("%q must be refused under the restricted profile, got %v", v, err)
		}
	}

	// A malformed value is rejected under either profile.
	if _, err := (Security{}).withRunAsUser("1000:"); !errors.Is(err, models.ErrRunAsUserInvalid) {
		t.Errorf("expected ErrRunAsUserInvalid, got %v", err)
	}

	// Blank leaves the resolved profile exactly as it was.
	if got, err := restricted.withRunAsUser("  "); err != nil || got.User != "100000:0" {
		t.Errorf("blank should keep the profile UID, got %+v (%v)", got, err)
	}
}

func TestContainerSecurityResolver(t *testing.T) {
	app := &models.Application{WorkspaceID: 7}

	// Nil resolver = no restriction (today's behavior).
	b := &runtimeBuilder{}
	if b.containerSecurity(app).Restricted {
		t.Error("nil resolver must yield no restriction")
	}

	// A resolver keyed on workspace id is consulted. An official-template app in a
	// workspace that exempts them keeps the image user; others are hardened.
	b.SetSecurity(SecurityFunc(func(id uint, official bool) Security {
		if id == 7 && !official {
			return Security{User: "100000:0", NoNewPrivileges: true, Restricted: true}
		}
		return Security{}
	}), "busybox:latest")
	if got := b.containerSecurity(app); got.User != "100000:0" {
		t.Errorf("resolver not consulted, got %+v", got)
	}
	if got := b.containerSecurity(&models.Application{WorkspaceID: 7, OfficialTemplate: true}); got.Restricted {
		t.Errorf("official-template app should be exempt, got %+v", got)
	}
	if b.containerSecurity(&models.Application{WorkspaceID: 9}).Restricted {
		t.Error("non-restricted workspace should not be hardened")
	}
}
