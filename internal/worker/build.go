// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

// ImageRefResolver resolves a platform-image catalog key to its effective ref (admin override ->
// config default -> built-in). Kept as an interface so the worker doesn't import the settings
// stack. Builds themselves run on the runner; the control plane only passes it the builder image.
type ImageRefResolver interface {
	Ref(key string) string
}

// BuildResult is what a runner build reports for provenance: the pushed image
// digest, its size when known, and which runner built it.
type BuildResult struct {
	Digest string
	Size   int64
	Runner string
}
