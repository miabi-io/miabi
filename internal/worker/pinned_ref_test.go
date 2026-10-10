// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestIsPinnedRef(t *testing.T) {
	const pin = "sha256:aaaa"
	pinned := &models.Application{Metadata: models.Metadata{models.MetaDigest: pin}}
	cases := []struct {
		name string
		app  *models.Application
		ref  string
		want bool
	}{
		{"deploy of the pin", pinned, "registry.example.com/ws_3/web@" + pin, true},
		{"tag deploy of a pinned app", pinned, "registry.example.com/ws_3/web:2.0.0", false},
		{"another digest", pinned, "registry.example.com/ws_3/web@sha256:bbbb", false},
		{"unpinned app", &models.Application{}, "registry.example.com/ws_3/web@" + pin, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPinnedRef(c.app, c.ref); got != c.want {
				t.Errorf("isPinnedRef(%q) = %v, want %v", c.ref, got, c.want)
			}
		})
	}
}
