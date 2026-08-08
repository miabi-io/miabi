// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package web embeds the built frontend (the Vue SPA) into the Go binary so a single `miabi`
// executable serves both the API and the UI. `dist/` is a build artifact: only a .gitkeep
// placeholder is committed, so `go build` works even when the UI has not been built.
package web

import "embed"

// Assets holds the built SPA under a top-level "dist/" directory, served via Okapi's WebFS
// with WebConfig{Root: "dist"}. The `all:` prefix keeps dotted files (the .gitkeep
// placeholder, Vite's occasional dotfiles) in the embed.
//
//go:embed all:dist
var Assets embed.FS
