// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// SecretWriter manages the Vault secrets owned by a managed database. Implemented by the secret
// service; injected after construction. Optional — nil disables auto-provisioning and the cascade,
// falling back to plaintext env injection.
type SecretWriter interface {
	UpsertOwned(workspaceID uint, ownerKind string, ownerID uint, name, value, description string) (*models.Secret, error)
	DeleteOwned(workspaceID uint, ownerKind string, ownerID uint) ([]models.Application, error)
}

// PasswordSecretName / URLSecretName are the canonical Vault names for a logical
// database's scoped-user password and full connection URL. The instance slug is
// unique per workspace, so the names don't collide across instances.
func PasswordSecretName(inst *models.DatabaseInstance, db *models.Database) string {
	return secretName(inst.Name, db.Name, "password")
}

func URLSecretName(inst *models.DatabaseInstance, db *models.Database) string {
	return secretName(inst.Name, db.Name, "url")
}

// InstancePasswordSecretName / InstanceURLSecretName are the names for an
// instance-level secret (Redis, which has no logical databases).
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
