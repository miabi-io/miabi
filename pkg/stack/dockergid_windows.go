// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later
//go:build windows

package stack

// dockerGID has no meaning on Windows, which has no socket ownership to inherit. The manifest field
// stays empty and the control plane runs as its image default.
func dockerGID() string { return "" }
