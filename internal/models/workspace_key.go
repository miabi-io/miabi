// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// WorkspaceKey is a workspace's data-encryption key (DEK), stored wrapped by the master KEK.
// The DEK encrypts the workspace's secrets at rest; the KEK only wraps the DEK. Rotation adds
// a new active version and re-encrypts to it. Exactly one version per workspace is Active.
type WorkspaceKey struct {
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index:idx_wskey_ws_ver,unique;not null"`
	Version     int  `json:"version" gorm:"index:idx_wskey_ws_ver,unique;not null"`
	// WrappedDEK is the DEK encrypted (AES-GCM) under the master KEK — i.e.
	// crypto.Encrypt(base64(dek)). Never returned by the API.
	WrappedDEK string `json:"-" gorm:"type:text;not null"`
	// Active marks the version new writes encrypt with. Older versions are kept so
	// existing ciphertext stays decryptable until a rotation sweep migrates it.
	Active bool `json:"active" gorm:"not null;default:true;index"`

	CreatedAt time.Time `json:"created_at"`
	RotatedAt time.Time `json:"rotated_at"`
}
