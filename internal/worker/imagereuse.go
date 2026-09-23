// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// ErrCachedImageUnauthorized refuses a workload that would run an image only because it is already
// on the node. It names the pull policy, because that is the knob the operator has to change.
var ErrCachedImageUnauthorized = errors.New(
	"this workspace holds no credential for that image's registry, so it may not run a copy cached on " +
		"the node by another workload — attach the registry credential to the app, or use a pull policy " +
		"that fetches the image")

// mayReuseCachedImage reports whether an image already present on the node may be run for this
// workload without pulling it.
//
// Presence proves only that SOMEBODY pulled it. Nodes are shared, so that somebody may be another
// workspace, and reusing their cached private image would let anyone who can spell the reference
// run it. Two things settle the question without a round trip:
//
//   - a ref in the platform's own registry is namespaced per workspace and was authorized upstream
//     by ResolveImageRef, which refuses a foreign namespace before the deploy gets this far;
//   - a credential the workspace holds for the SAME registry host means it could pull the image
//     itself anyway, so reusing the local copy grants nothing new.
//
// Anything else is pulled and the registry decides: a public image succeeds, a private one fails and
// takes the deploy with it. That costs a round trip on a cache hit, which is the price of not
// serving another tenant's image from a shared disk.
func mayReuseCachedImage(ref string, internal bool, auth *docker.RegistryAuth) bool {
	if internal {
		return true
	}
	if auth == nil {
		return false
	}
	return registryHostOf(ref) == registryHostOf(authServer(auth.Server))
}

// dockerHub is the canonical host every Docker Hub spelling collapses to.
const dockerHub = "docker.io"

// registryHostOf is the registry a reference addresses. Docker reads the first path segment as a
// host only when it looks like one — it carries a dot or a port, or it is localhost — and treats
// anything else as a Docker Hub namespace, which is why `alpine` and `library/alpine` are the same
// image. Getting this wrong in the permissive direction would let a credential for one registry
// wave through a cached image from another.
func registryHostOf(ref string) string {
	ref = strings.TrimSpace(ref)
	head, _, found := strings.Cut(ref, "/")
	if !found || !looksLikeHost(head) {
		return dockerHub
	}
	host := strings.ToLower(head)
	switch host {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com", dockerHub:
		return dockerHub
	}
	return host
}

func looksLikeHost(seg string) bool {
	return strings.ContainsAny(seg, ".:") || seg == "localhost"
}

// authServer reduces a stored registry Server to something registryHostOf can read. Credentials are
// written by hand, so the field turns up as a bare host, with a scheme, or as Docker's own
// "https://index.docker.io/v1/" — all of which must resolve to the same host as the reference does.
func authServer(server string) string {
	s := strings.ToLower(strings.TrimSpace(server))
	if _, rest, found := strings.Cut(s, "://"); found {
		s = rest
	}
	s, _, _ = strings.Cut(s, "/")
	if s == "" {
		return dockerHub
	}
	// registryHostOf reads a bare single-label string as a Docker Hub namespace; a credential's
	// Server is always a host, so give it one it will recognise as such.
	if !looksLikeHost(s) {
		return dockerHub
	}
	return s + "/"
}
