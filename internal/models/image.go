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

import "time"

// Image is a built artifact; GC never collects a digest a live deployment or pinned release references.
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
