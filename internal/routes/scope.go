// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// scopeFor derives the API-key scope a route requires from its method and its full template path
func scopeFor(method, path string) string {
	segs := segments(path)
	if s := adminScope(method, path, segs); s != "" {
		return s
	}
	if method == "GET" || method == "HEAD" {
		return models.ScopeRead
	}
	if isDeployAction(segs) {
		return models.ScopeDeploy
	}
	return models.ScopeWrite
}

// adminPrefixes are whole route trees that administer the platform or the account itself.
var adminPrefixes = []string{
	"/api/v1/admin/",
	"/api/v1/api-keys",
	"/api/v1/auth/2fa/",
	"/api/v1/me/sessions",
	"/api/v1/scim/",
	"/api/v1/sso/",
}

// adminSegments make a route administrative wherever they appear: who may act in a workspace, and
// what the platform recorded about it.
var adminSegments = map[string]bool{
	"members":         true,
	"invitations":     true,
	"roles":           true,
	"policies":        true,
	"api-keys":        true,
	"backup-settings": true,
	"audit":           true,
	"audit-logs":      true,
	"portable-backup": true,
}

// adminSuffixes are reads that hand back a credential, a key or a shell — the GETs the plan calls
// out, because classifying them by method alone would make them `read`.
var adminSuffixes = []string{
	"/exec",
	"/reveal",
	"/credentials",
	"/connection",
	"/recovery-kit",
	"/webhook-info",
}

// adminWriteSegments are trees where reading is ordinary but writing reconfigures how the platform
// builds and who it calls out to.
var adminWriteSegments = map[string]bool{"runners": true, "webhooks": true}

// deployActions are the lifecycle verbs a `deploy` key exists to trigger.
var deployActions = map[string]bool{
	"deploy":   true,
	"start":    true,
	"stop":     true,
	"restart":  true,
	"rollback": true,
	"scale":    true,
	"trigger":  true,
	"rerun":    true,
	"sync":     true,
	"promote":  true,
	"approve":  true,
}

func adminScope(method, path string, segs []string) string {
	for _, p := range adminPrefixes {
		if strings.HasPrefix(path, p) {
			return models.ScopeAdmin
		}
	}
	for _, s := range adminSuffixes {
		if strings.HasSuffix(path, s) {
			return models.ScopeAdmin
		}
	}
	// Deleting a workspace takes its apps, data and members with it: `/api/v1/workspaces/{x}`.
	if method == "DELETE" && len(segs) == 4 && segs[2] == "workspaces" && isParam(segs[3]) {
		return models.ScopeAdmin
	}
	if isBackupDownload(segs) {
		return models.ScopeAdmin
	}
	for _, s := range segs {
		if adminSegments[s] {
			return models.ScopeAdmin
		}
		if adminWriteSegments[s] && method != "GET" && method != "HEAD" {
			return models.ScopeAdmin
		}
	}
	return ""
}

// isBackupDownload separates a backup artifact — the workspace's data, in one file — from the
// ordinary `/logs/download` that every deployment and job step offers.
func isBackupDownload(segs []string) bool {
	if len(segs) == 0 || segs[len(segs)-1] != "download" {
		return false
	}
	if len(segs) >= 2 && segs[len(segs)-2] == "logs" {
		return false
	}
	for _, s := range segs {
		if s == "backups" || s == "platform-backup" {
			return true
		}
	}
	return false
}

func isDeployAction(segs []string) bool {
	for _, s := range segs {
		if deployActions[s] || s == "canary" || strings.HasPrefix(s, "canary-") {
			return true
		}
	}
	return false
}

func segments(path string) []string {
	out := strings.Split(strings.Trim(path, "/"), "/")
	if len(out) == 1 && out[0] == "" {
		return nil
	}
	return out
}

func isParam(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}
