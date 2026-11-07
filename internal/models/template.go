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
	"time"

	"gorm.io/gorm"
)

// TemplateSourceType is where a set of templates comes from.
type TemplateSourceType string

const (
	TemplateSourceBuiltin TemplateSourceType = "builtin" // embedded official catalog
	TemplateSourceGit     TemplateSourceType = "git"     // a Git repo with an index.yaml
	TemplateSourceHTTP    TemplateSourceType = "http"    // a remote index.yaml
	TemplateSourceCustom  TemplateSourceType = "custom"  // per-workspace user imports
)

// Global sources (WorkspaceID nil) are admin-managed.
type TemplateSource struct {
	ID          uint               `json:"id" gorm:"primaryKey"`
	WorkspaceID *uint              `json:"workspace_id,omitempty" gorm:"index"`
	Name        string             `json:"name" gorm:"not null"`
	Type        TemplateSourceType `json:"type" gorm:"not null"`
	URL         string             `json:"url,omitempty"`
	Ref         string             `json:"ref,omitempty"`         // git branch/tag
	GitRepoID   *uint              `json:"git_repo_id,omitempty"` // optional stored credential
	Official    bool               `json:"official" gorm:"not null;default:false"`
	Verified    bool               `json:"verified" gorm:"not null;default:false"`
	SyncStatus  string             `json:"sync_status,omitempty"` // idle | syncing | error
	LastSynced  *time.Time         `json:"last_synced,omitempty"`
	LastError   string             `json:"last_error,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	DeletedAt   gorm.DeletedAt     `json:"-" gorm:"index"`
}

// The parsed form is re-derived on demand from RawYAML.
type Template struct {
	ID       uint `json:"id" gorm:"primaryKey"`
	SourceID uint `json:"source_id" gorm:"index:idx_template_src_name_ver,unique;not null"`
	// Name is the stable template handle (e.g. "ghost").
	Name string `json:"name" gorm:"index:idx_template_src_name_ver,unique;not null"`
	// Version is the manifest semver; unique per (source, name).
	Version string `json:"version" gorm:"index:idx_template_src_name_ver,unique;not null"`
	// DisplayName is the free-text catalog label (e.g. "Ghost").
	DisplayName string         `json:"display_name"`
	Category    string         `json:"category,omitempty"`
	Icon        string         `json:"icon,omitempty"`
	Digest      string         `json:"digest,omitempty"`
	RawYAML     string         `json:"-" gorm:"type:text"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

type TemplateInstall struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index;not null"`
	Source      string `json:"source"` // official | community | custom
	// TemplateName is the template handle installed from (references Template.Name).
	TemplateName string `json:"template_name" gorm:"index;not null"`
	// TemplateDisplayName is the catalog label snapshot at install time.
	TemplateDisplayName string `json:"template_display_name"`
	// Recorded so an upgrade names resources the new version adds the same way the install named the rest.
	DisplayName string `json:"display_name,omitempty"`
	Version     string `json:"version"`
	StackID     *uint  `json:"stack_id,omitempty" gorm:"index"`
	AppIDs      []uint `json:"app_ids,omitempty" gorm:"serializer:json"`
	DatabaseIDs []uint `json:"database_ids,omitempty" gorm:"serializer:json"`
	VolumeIDs   []uint `json:"volume_ids,omitempty" gorm:"serializer:json"`
	// A map because the workspace name may carry a uniqueness suffix that an upgrade cannot recompute.
	ConfigIDs map[string]uint `json:"config_ids,omitempty" gorm:"serializer:json"`
	// Inputs holds the non-secret install answers, for re-install/upgrade prompts.
	Inputs    map[string]string `json:"inputs,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time         `json:"created_at"`
}
