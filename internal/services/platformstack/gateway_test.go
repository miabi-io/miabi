// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformstack

import (
	"path/filepath"
	"strings"
	"testing"
)

// The crux of bind-mounting from inside the installer container: our /etc/miabi is a
// bind of SOME host directory. Handing the daemon our own view of the path would point
// it at a host path that may not exist — and Docker's answer to a missing bind source
// is to silently create a DIRECTORY. Goma would then find a folder where goma.yml
// should be and fail for a reason that looks nothing like the cause.
func TestHostPathTranslation(t *testing.T) {
	// Mirrors what InspectContainerConfig returns for the installer container.
	mounts := []struct{ dest, src string }{
		{"/etc/miabi", "/srv/miabi-config"}, // -v /srv/miabi-config:/etc/miabi
		{"/var/run/docker.sock", "/var/run/docker.sock"},
	}

	translate := func(path string) string {
		best, src := "", ""
		for _, m := range mounts {
			if path == m.dest || strings.HasPrefix(path, m.dest+"/") {
				if len(m.dest) > len(best) {
					best, src = m.dest, m.src
				}
			}
		}
		if best == "" {
			return path
		}
		return filepath.Join(src, strings.TrimPrefix(path, best))
	}

	for in, want := range map[string]string{
		"/etc/miabi/goma.yml":   "/srv/miabi-config/goma.yml",
		"/etc/miabi/stack.yaml": "/srv/miabi-config/stack.yaml",
		"/etc/miabi":            "/srv/miabi-config",
		// Not under any bind: it exists only inside the container, and handing it to the
		// daemon would have Docker invent an empty directory on the host.
		"/tmp/elsewhere.yml": "/tmp/elsewhere.yml",
	} {
		if got := translate(in); got != want {
			t.Errorf("translate(%q) = %q, want %q", in, got, want)
		}
	}
}

// Absent → write the default. Unmodified → a newer default replaces it, so installs
// that never customized still get upstream fixes. Modified → never touched again.
func TestGatewayConfigPolicy(t *testing.T) {
	const shipped, newer = "sha-of-shipped", "sha-of-newer"

	decide := func(fileSHA, recordedSHA, defaultSHA string) string {
		switch {
		case fileSHA == "":
			return "write-default"
		case recordedSHA != "" && fileSHA == recordedSHA:
			if defaultSHA != recordedSHA {
				return "refresh"
			}
			return "leave"
		default:
			return "keep-custom"
		}
	}

	for _, c := range []struct{ name, file, recorded, def, want string }{
		{"absent", "", "", shipped, "write-default"},
		{"unmodified, default unchanged", shipped, shipped, shipped, "leave"},
		{"unmodified, NEW default", shipped, shipped, newer, "refresh"},
		{"operator edited it", "sha-of-their-edit", shipped, newer, "keep-custom"},
		{"predates ConfigSHA", shipped, "", shipped, "keep-custom"},
	} {
		if got := decide(c.file, c.recorded, c.def); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
