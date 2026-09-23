// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

func cred(server string) *docker.RegistryAuth {
	return &docker.RegistryAuth{Server: server, Username: "u", Password: "p"}
}

// The finding: an image cached on a shared node by one workspace must not run for another just
// because it is on disk. Without a credential for its registry, the copy is not this workspace's
// to use.
func TestMayReuseCachedImageRefusesAForeignPrivateImage(t *testing.T) {
	for _, ref := range []string{
		"ghcr.io/acme/private:1.2.3",
		"ghcr.io/acme/private@sha256:aaaa",
		"registry.example.com:5000/team/app:latest",
		"acme/private:1",     // Docker Hub, implicit host
		"private:1",          // Docker Hub official-namespace spelling
		"localhost:5000/x:1", // a node-local registry is still not ours
	} {
		if mayReuseCachedImage(ref, false, nil) {
			t.Errorf("%s: reused a cached image with no credential for its registry", ref)
		}
	}
}

// A credential for the same registry means the workspace could pull the image itself, so reusing
// the local copy grants it nothing it did not already have.
func TestMayReuseCachedImageAllowsAHostTheWorkspaceCanPullFrom(t *testing.T) {
	for _, tc := range []struct{ ref, server string }{
		{"ghcr.io/acme/app:1", "ghcr.io"},
		{"ghcr.io/acme/app:1", "https://ghcr.io"},
		{"registry.example.com:5000/t/a:1", "registry.example.com:5000"},
		{"GHCR.io/acme/app:1", "ghcr.io"},
		// Every Docker Hub spelling has to reduce to the same host, or a Hub credential
		// would not match a Hub image.
		{"acme/app:1", "docker.io"},
		{"acme/app:1", "index.docker.io"},
		{"acme/app:1", "https://index.docker.io/v1/"},
		{"docker.io/acme/app:1", "registry-1.docker.io"},
		{"app:1", "docker.io"},
	} {
		if !mayReuseCachedImage(tc.ref, false, cred(tc.server)) {
			t.Errorf("ref %q with a credential for %q was refused", tc.ref, tc.server)
		}
	}
}

// The dangerous direction: a credential for one registry must not wave through a cached image from
// another. Attaching a throwaway Docker Hub credential would otherwise unlock every private image
// on the node.
func TestMayReuseCachedImageRefusesAMismatchedHost(t *testing.T) {
	for _, tc := range []struct{ ref, server string }{
		{"ghcr.io/acme/private:1", "docker.io"},
		{"ghcr.io/acme/private:1", "registry.example.com"},
		{"acme/private:1", "ghcr.io"},
		{"registry.example.com:5000/t/a:1", "registry.example.com"}, // a different port is a different registry
		{"registry.example.com/t/a:1", "evil.example.com"},
	} {
		if mayReuseCachedImage(tc.ref, false, cred(tc.server)) {
			t.Errorf("ref %q was reused on a credential for %q", tc.ref, tc.server)
		}
	}
}

// The platform's own registry namespaces every repository per workspace, and ResolveImageRef has
// already refused a foreign one before the deploy reaches the pull decision.
func TestMayReuseCachedImageAllowsTheInternalRegistry(t *testing.T) {
	if !mayReuseCachedImage("registry.example.com/ws_7/app:1", true, nil) {
		t.Error("an internal, namespace-authorized ref was refused")
	}
}

func TestRegistryHostOf(t *testing.T) {
	for ref, want := range map[string]string{
		"alpine":                          dockerHub,
		"alpine:3.19":                     dockerHub,
		"library/alpine:3.19":             dockerHub,
		"acme/app:1":                      dockerHub,
		"docker.io/acme/app:1":            dockerHub,
		"index.docker.io/acme/app:1":      dockerHub,
		"registry-1.docker.io/acme/app:1": dockerHub,
		"ghcr.io/acme/app:1":              "ghcr.io",
		"GHCR.IO/acme/app:1":              "ghcr.io",
		"registry.example.com/a/b:1":      "registry.example.com",
		"registry.example.com:5000/a:1":   "registry.example.com:5000",
		"localhost:5000/a:1":              "localhost:5000",
		"localhost/a:1":                   "localhost",
	} {
		if got := registryHostOf(ref); got != want {
			t.Errorf("registryHostOf(%q) = %q, want %q", ref, got, want)
		}
	}
}
