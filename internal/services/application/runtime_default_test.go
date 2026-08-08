// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// TestDefaultToServiceRuntime pins the cluster-mode create default: an unspecified runtime becomes a
// service only when cluster mode is on AND the create is interactive. Any explicit choice opts out,
// and declarative sources are excluded so they stay deterministic.
func TestDefaultToServiceRuntime(t *testing.T) {
	cases := []struct {
		name        string
		explicit    models.RuntimeKind
		clusterOn   bool
		interactive bool
		want        bool
	}{
		{"unspecified + cluster on + interactive -> service", "", true, true, true},
		{"unspecified + cluster on + declarative -> stays container", "", true, false, false},
		{"unspecified + cluster off + interactive -> container", "", false, true, false},
		{"explicit container + cluster on + interactive -> opt out", models.RuntimeContainer, true, true, false},
		{"explicit service + cluster on -> already service", models.RuntimeService, true, true, false},
		{"explicit container + cluster off", models.RuntimeContainer, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultToServiceRuntime(tc.explicit, tc.clusterOn, tc.interactive); got != tc.want {
				t.Fatalf("defaultToServiceRuntime(%q, %v, %v) = %v, want %v", tc.explicit, tc.clusterOn, tc.interactive, got, tc.want)
			}
		})
	}
}

// TestHasNodeLocalStorageHostBind covers the volume-free branches (a privileged
// host bind is node-local; no mounts is not) without needing a volume repo.
func TestHasNodeLocalStorageHostBind(t *testing.T) {
	s := &Service{}
	if !s.hasNodeLocalStorage(&models.Application{Mounts: []models.AppMount{{HostPreset: "docker-sock", Path: "/x"}}}) {
		t.Fatal("a host-preset bind must count as node-local storage")
	}
	if s.hasNodeLocalStorage(&models.Application{}) {
		t.Fatal("an app with no mounts must not count as node-local")
	}
}

// TestRequireSharedStorageHostBind covers the host-bind rejection for a service
// (any replica count) and the container/no-mount no-ops — none touch the volume
// repo, so a zero-value Service is enough.
func TestRequireSharedStorageHostBind(t *testing.T) {
	s := &Service{}
	svcWithBind := &models.Application{
		RuntimeKind: models.RuntimeService,
		Mounts:      []models.AppMount{{HostPreset: "docker-sock", Path: "/x"}},
	}
	if err := s.requireSharedStorage(svcWithBind, 1); !errors.Is(err, ErrHostBindService) {
		t.Fatalf("service + host bind (1 replica) = %v, want ErrHostBindService", err)
	}
	container := &models.Application{RuntimeKind: models.RuntimeContainer, Mounts: svcWithBind.Mounts}
	if err := s.requireSharedStorage(container, 1); err != nil {
		t.Fatalf("container app with a host bind must be allowed, got %v", err)
	}
	if err := s.requireSharedStorage(&models.Application{RuntimeKind: models.RuntimeService}, 3); err != nil {
		t.Fatalf("service with no mounts must be allowed, got %v", err)
	}
}
