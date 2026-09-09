// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Image is a built container artifact with provenance. Deploy-by-digest records
// exactly what ran; the catalog/GC reasons about retention without ever
// collecting a digest a live deployment or pinned release references.
type Image struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID uint   `json:"workspace_id" gorm:"index:idx_image_ws_digest,unique;not null"`
	Repository  string `json:"repository" gorm:"not null"`
	Digest      string `json:"digest" gorm:"index:idx_image_ws_digest,unique;not null"` // sha256:…
	Tag         string `json:"tag,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`

	PipelineRunID *uint      `json:"pipeline_run_id,omitempty" gorm:"index"`
	ApplicationID *uint      `json:"application_id,omitempty" gorm:"index"`
	Commit        string     `json:"commit,omitempty"`
	Runner        string     `json:"runner,omitempty"`
	BuiltAt       *time.Time `json:"built_at,omitempty"`

	// SBOM / Signature attach supply-chain metadata. Never serialized.
	SBOM      string `json:"-" gorm:"type:text"`
	Signature string `json:"-" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`
}

// Ref returns the canonical repository@digest pin.
func (i *Image) Ref() string {
	if i.Digest == "" {
		return i.Repository
	}
	return i.Repository + "@" + i.Digest
}
