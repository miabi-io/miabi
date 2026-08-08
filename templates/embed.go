// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package templates embeds a minimal offline floor of the official Miabi marketplace catalog — the
// core data and infra primitives — so the binary works out of the box without network access. The
// full catalog lives in the marketplace registry and is merged on top at runtime.
package templates

import "embed"

// FS holds the registry index and every embedded template manifest.
//
//go:embed index.yaml libsql minio mongodb mysql nginx postgresql redis
var FS embed.FS
