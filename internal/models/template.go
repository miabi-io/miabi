// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// TemplateSource is where templates come from. Global sources (WorkspaceID nil)
// are admin-managed; a custom source is per-workspace.
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

// Template is one version of a catalog entry, synced from a source. RawYAML is
// the manifest as authored; the parsed form is re-derived on demand.
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

// TemplateInstall records that a stack/app(s) were installed from a template, so
// the UI can show provenance ("installed from Ghost 1.2.0") and later offer
// upgrade/uninstall.
type TemplateInstall struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index;not null"`
	Source      string `json:"source"` // official | community | custom
	// TemplateName is the template handle installed from (references Template.Name).
	TemplateName string `json:"template_name" gorm:"index;not null"`
	// TemplateDisplayName is the catalog label snapshot at install time.
	TemplateDisplayName string `json:"template_display_name"`
	// DisplayName is the label the user gave *this install*, which every resource
	// it creates is named after. Recorded so an upgrade can name a resource the
	// new version adds the same way the install named the rest.
	DisplayName string `json:"display_name,omitempty"`
	Version     string `json:"version"`
	StackID     *uint  `json:"stack_id,omitempty" gorm:"index"`
	AppIDs      []uint `json:"app_ids,omitempty" gorm:"serializer:json"`
	DatabaseIDs []uint `json:"database_ids,omitempty" gorm:"serializer:json"`
	VolumeIDs   []uint `json:"volume_ids,omitempty" gorm:"serializer:json"`
	// ConfigIDs maps a template-local config name to the workspace config created
	// for it. A map rather than a list of ids because an upgrade has to resolve
	// "the config this version calls X" — and the workspace name it was given may
	// carry a uniqueness suffix that cannot be recomputed.
	ConfigIDs map[string]uint `json:"config_ids,omitempty" gorm:"serializer:json"`
	// Inputs holds the non-secret install answers, for re-install/upgrade prompts.
	Inputs    map[string]string `json:"inputs,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time         `json:"created_at"`
}
