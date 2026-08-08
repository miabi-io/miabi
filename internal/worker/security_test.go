// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
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

func TestPrepareRestrictedVolumes(t *testing.T) {
	b := &runtimeBuilder{securityInitImage: "busybox:latest"}
	restricted := Security{User: "100000:0"}
	mounts := map[string]string{"vol1": "/data"}

	// Not restricted: no chown container runs at all.
	f := &chownFake{}
	if err := b.prepareRestrictedVolumes(context.Background(), f, Security{}, "wordpress:6", mounts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.oneShotImages) != 0 {
		t.Errorf("no-op expected for the default profile, ran %v", f.oneShotImages)
	}

	// Restricted, app image can chown: the app image seeds + chowns; busybox unused.
	f = &chownFake{}
	if err := b.prepareRestrictedVolumes(context.Background(), f, restricted, "wordpress:6", mounts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.oneShotImages) != 1 || f.oneShotImages[0] != "wordpress:6" {
		t.Errorf("expected chown via the app image only, got %v", f.oneShotImages)
	}

	// Restricted, app image lacks chown: falls back to the busybox init image, which
	// corrects ownership of the volume the app image already seeded.
	f = &chownFake{failImage: "distroless:app"}
	if err := b.prepareRestrictedVolumes(context.Background(), f, restricted, "distroless:app", mounts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.oneShotImages) != 2 || f.oneShotImages[0] != "distroless:app" || f.oneShotImages[1] != "busybox:latest" {
		t.Errorf("expected app image then busybox fallback, got %v", f.oneShotImages)
	}
}

func TestSecurityApplyTo(t *testing.T) {
	// Zero value = no restriction; RunSpec stays at image defaults.
	var spec docker.RunSpec
	(Security{}).applyTo(&spec)
	if spec.User != "" || spec.NoNewPrivileges || spec.CapDrop != nil {
		t.Errorf("zero Security should leave the spec untouched, got %+v", spec)
	}
	if (Security{}).Restricted() {
		t.Error("zero Security must not report restricted")
	}

	// Restricted profile stamps user + hardening.
	sec := Security{User: "100000:0", NoNewPrivileges: true, CapDrop: []string{"NET_RAW"}}
	sec.applyTo(&spec)
	if !sec.Restricted() {
		t.Error("Security with a user should report restricted")
	}
	if spec.User != "100000:0" || !spec.NoNewPrivileges || len(spec.CapDrop) != 1 {
		t.Errorf("restricted Security not applied: %+v", spec)
	}
}

func TestContainerSecurityResolver(t *testing.T) {
	app := &models.Application{WorkspaceID: 7}

	// Nil resolver = no restriction (today's behavior).
	b := &runtimeBuilder{}
	if b.containerSecurity(app).Restricted() {
		t.Error("nil resolver must yield no restriction")
	}

	// A resolver keyed on workspace id is consulted. An official-template app in a
	// workspace that exempts them keeps the image user; others are hardened.
	b.SetSecurity(SecurityFunc(func(id uint, official bool) Security {
		if id == 7 && !official {
			return Security{User: "100000:0", NoNewPrivileges: true}
		}
		return Security{}
	}), "busybox:latest")
	if got := b.containerSecurity(app); got.User != "100000:0" {
		t.Errorf("resolver not consulted, got %+v", got)
	}
	if got := b.containerSecurity(&models.Application{WorkspaceID: 7, OfficialTemplate: true}); got.Restricted() {
		t.Errorf("official-template app should be exempt, got %+v", got)
	}
	if b.containerSecurity(&models.Application{WorkspaceID: 9}).Restricted() {
		t.Error("non-restricted workspace should not be hardened")
	}
}
