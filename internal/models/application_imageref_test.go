// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "testing"

func TestApplicationImageRef(t *testing.T) {
	const digest = "sha256:0aaa505af7889cc4d1e9eecd7ddf5b2f4034102535b94bc5fd97af50741194b5"
	cases := []struct {
		name     string
		app      Application
		override string
		want     string
	}{
		{"tag", Application{Image: "ghcr.io/acme/web", Tag: "1.4.0"}, "", "ghcr.io/acme/web:1.4.0"},
		{"no tag is latest", Application{Image: "ghcr.io/acme/web"}, "", "ghcr.io/acme/web:latest"},
		{"override wins over tag", Application{Image: "ghcr.io/acme/web", Tag: "1.4.0"}, "2.0.0", "ghcr.io/acme/web:2.0.0"},
		{
			"pinned digest wins over tag",
			Application{Image: "ghcr.io/acme/web", Tag: "latest", Metadata: Metadata{MetaDigest: digest}},
			"", "ghcr.io/acme/web@" + digest,
		},
		{
			"explicit tag deploy wins over the pin",
			Application{Image: "ghcr.io/acme/web", Tag: "latest", Metadata: Metadata{MetaDigest: digest}},
			"2.0.0", "ghcr.io/acme/web:2.0.0",
		},
		{
			"cleared pin falls back to the tag",
			Application{Image: "ghcr.io/acme/web", Tag: "1.4.0", Metadata: Metadata{MetaDigest: ""}},
			"", "ghcr.io/acme/web:1.4.0",
		},
		{
			"image already pinned is used verbatim",
			Application{Image: "ghcr.io/acme/web@" + digest, Metadata: Metadata{MetaDigest: "sha256:other"}},
			"", "ghcr.io/acme/web@" + digest,
		},
		{"git app has no image", Application{Metadata: Metadata{MetaDigest: digest}}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.app.ImageRef(c.override); got != c.want {
				t.Errorf("ImageRef(%q) = %q, want %q", c.override, got, c.want)
			}
		})
	}
}
