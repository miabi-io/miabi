// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UIDModel is an embeddable mixin giving a resource a stable UUIDv7 alongside its uint primary
// key: the PK stays the internal handle, the uid is the portable external one (Terraform id,
// GitOps/backup reference, non-enumerable public handle). Embed it anonymously.
type UIDModel struct {
	UID string `json:"uid" gorm:"type:uuid;uniqueIndex;not null;default:gen_random_uuid()"`
}

// BeforeCreate assigns a UUIDv7 when one was not supplied. A caller may preset
// UID (e.g. import/restore keeping an incoming identity); only an empty value is
// generated.
func (m *UIDModel) BeforeCreate(*gorm.DB) error {
	if m.UID == "" {
		v, err := uuid.NewV7()
		if err != nil {
			return err
		}
		m.UID = v.String()
	}
	return nil
}
