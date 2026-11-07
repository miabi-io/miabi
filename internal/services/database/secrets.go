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

package database

import (
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// Owner kinds for a managed database's Vault secrets.
const (
	SecretOwnerDatabase = "database"    // a logical database (its scoped user)
	SecretOwnerInstance = "db_instance" // an instance (redis / admin)
)

// SecretWriter manages Vault secrets owned by a database; nil falls back to plaintext env injection.
type SecretWriter interface {
	UpsertOwned(workspaceID uint, ownerKind string, ownerID uint, name, value, description string) (*models.Secret, error)
	DeleteOwned(workspaceID uint, ownerKind string, ownerID uint) ([]models.Application, error)
}

// The instance slug is unique per workspace, so secret names don't collide across instances.
func PasswordSecretName(inst *models.DatabaseInstance, db *models.Database) string {
	return secretName(inst.Name, db.Name, "password")
}

func URLSecretName(inst *models.DatabaseInstance, db *models.Database) string {
	return secretName(inst.Name, db.Name, "url")
}

func InstancePasswordSecretName(inst *models.DatabaseInstance) string {
	return secretName(inst.Name, "", "password")
}

func InstanceURLSecretName(inst *models.DatabaseInstance) string {
	return secretName(inst.Name, "", "url")
}

func secretName(instSlug, dbName, suffix string) string {
	name := "db_" + sanitizeSecretPart(instSlug)
	if dbName != "" {
		name += "_" + sanitizeSecretPart(dbName)
	}
	name += "_" + suffix
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

func sanitizeSecretPart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
