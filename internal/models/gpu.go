// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// "amd" is reserved for a later ROCm phase.
type GPUVendor string

const (
	GPUVendorNvidia GPUVendor = "nvidia"
	GPUVendorAMD    GPUVendor = "amd" // reserved (ROCm), not yet supported
)

// GPUDevice is keyed by the stable nvidia-smi UUID, never the host-local index, which changes across reboots.
type GPUDevice struct {
	ID       uint `json:"id" gorm:"primaryKey"`
	ServerID uint `json:"server_id" gorm:"index;not null"` // the node it lives on

	UUID     string    `json:"uuid" gorm:"uniqueIndex;not null"`
	Index    int       `json:"index"` // host-local device index at last inventory (informational)
	Vendor   GPUVendor `json:"vendor" gorm:"not null;default:nvidia"`
	Model    string    `json:"model"`     // "NVIDIA A100-SXM4-40GB"
	MemoryMB int       `json:"memory_mb"` // total framebuffer memory

	// Newly discovered cards arrive disabled (fail closed).
	Enabled bool `json:"enabled" gorm:"not null;default:false"`
	// A dedicated card is held by at most one app; the app's own canary and redeploy may share it.
	Shared bool `json:"shared" gorm:"not null;default:true"`

	// Set only by a successful probe that no longer lists the card; the row is kept so admin flags survive its return.
	Missing    bool       `json:"missing" gorm:"not null;default:false"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// An empty kind matches any; otherwise vendor, exact model, or case-insensitive model substring.
func (d GPUDevice) MatchesKind(kind string) bool {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return true
	}
	return strings.EqualFold(string(d.Vendor), kind) ||
		strings.EqualFold(d.Model, kind) ||
		strings.Contains(strings.ToLower(d.Model), strings.ToLower(kind))
}

var ErrGPUCapabilityInvalid = errors.New("invalid gpu capability")

// Mirrors the set dockerd's NVIDIA driver forwards as NVIDIA_DRIVER_CAPABILITIES; anything else is rejected at device request time.
var gpuDriverCapabilities = map[string]bool{
	"compute": true, "utility": true, "video": true, "graphics": true, "display": true, "compat32": true,
}

// NormalizeGPUCapabilities lowercases, dedupes and sorts; empty keeps the toolkit default (compute, utility).
func NormalizeGPUCapabilities(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		if !gpuDriverCapabilities[c] {
			return nil, fmt.Errorf("%w %q (allowed: compute, utility, video, graphics, display, compat32)", ErrGPUCapabilityInvalid, c)
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, nil
	}
	sort.Strings(out)
	return out, nil
}
