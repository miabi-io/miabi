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

package storage

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// InstallIDKey is the settings key holding this deployment's stable Install ID.
const InstallIDKey = "install_id"

// Immutable for the deployment's life (quoted when buying a license); FirstOrCreate makes concurrent boots safe.
func EnsureInstallID(db *gorm.DB) (string, error) {
	var existing models.Setting
	err := db.Where("key = ?", InstallIDKey).First(&existing).Error
	if err == nil && strings.TrimSpace(existing.Value) != "" {
		return existing.Value, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	id := "mbi_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	rec := models.Setting{Key: InstallIDKey}
	if err := db.Where(models.Setting{Key: InstallIDKey}).
		Attrs(models.Setting{Value: id, Type: models.SettingTypeString}).
		FirstOrCreate(&rec).Error; err != nil {
		return "", err
	}
	logger.Info("install id ready", "install_id", rec.Value)
	return rec.Value, nil
}

// Lets restore refuse a dump into an older binary: a downgrade restore corrupts silently.
func SchemaVersion(db *gorm.DB) string {
	var step models.UpgradeStep
	if err := db.Order("applied_at DESC, id DESC").First(&step).Error; err != nil {
		return ""
	}
	return step.Version
}

// Boot must stop: an install without an admin would make whoever registers first the platform admin.
var ErrAdminSeedInvalid = errors.New("cannot seed the platform admin")

// SeedAdmin is idempotent; validate runs only when an admin is about to be created.
func SeedAdmin(db *gorm.DB, email, password string, validate func() error) (*models.User, error) {
	// Already bootstrapped: return the first admin (its workspace is then ensured).
	var existing models.User
	err := db.Where("role = ?", models.SystemRoleAdmin).Order("id ASC").First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	if validate != nil {
		if err := validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrAdminSeedInvalid, err)
		}
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return nil, fmt.Errorf("%w: admin email and password are required to seed the first account", ErrAdminSeedInvalid)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash admin password: %w", err)
	}
	now := time.Now()
	admin := &models.User{
		Name:            "Platform Admin",
		Username:        "admin",
		Email:           email,
		PasswordHash:    string(hash),
		Role:            models.SystemRoleAdmin,
		Active:          true,
		EmailVerifiedAt: &now,
		SignupSource:    models.SignupSourceAdmin,
	}
	if err := db.Create(admin).Error; err != nil {
		return nil, fmt.Errorf("create admin user: %w", err)
	}
	logger.Info("seeded platform admin", "email", email)
	return admin, nil
}

// The default org predates any user, and the first admin is a deterministic owner unlike first sign-in.
func AdoptDefaultOrganizationOwner(db *gorm.DB, adminID uint) error {
	if adminID == 0 {
		return nil
	}
	res := db.Model(&models.Organization{}).
		Where("is_default = ? AND owner_user_id = ?", true, 0).
		Update("owner_user_id", adminID)
	if res.Error != nil {
		return fmt.Errorf("adopt default organization owner: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		logger.Info("the platform admin now owns the default organization", "user", adminID)
	}
	return nil
}

// A no-op once any plan exists, so admin edits are preserved; -1 means unlimited, 0 none.
func SeedPlans(db *gorm.DB) error {
	var n int64
	if err := db.Model(&models.Plan{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	const u = models.Unlimited
	plans := []models.Plan{
		{
			Name: "Pro", Description: "Higher limits and custom TLS for production.",
			IsDefault:                 true,
			IsActive:                  true,
			MaxApps:                   25,
			MaxDatabaseInstances:      10,
			MaxCronJobs:               50,
			MaxVolumes:                50,
			MaxNetworks:               10,
			MaxAPIKeys:                25,
			MaxMembers:                25,
			MaxDatabasesPerInstance:   20,
			MaxCPUCores:               16,
			MaxRunners:                3,
			MaxMemoryMB:               32768,
			MaxDatabaseInstanceSizeMB: 51200, MaxStorageMB: 512000,
			MaxDatabaseCPUCores: 10, MaxDatabaseMemoryMB: 20480,
			AllowCustomTLS: true, AllowPrivilegedHostMounts: false, AllowShellExec: true, AllowSharedStorage: true, AllowDNSProviders: true, AllowCustomLabels: true,
			AllowOfficialImageUser: true,
			AllowPlatformRunners:   true,
			AllowServiceRuntime:    true,
		},
		{
			// The platform's own plan for the system workspace, not part of the operator's catalog.
			Name: models.UnlimitedPlanName, Description: "No resource limits; all capabilities.", IsActive: true, System: true,
			MaxApps: u, MaxDatabaseInstances: u, MaxCronJobs: u, MaxVolumes: u, MaxNetworks: u, MaxAPIKeys: u, MaxMembers: u,
			MaxRunners:              3,
			MaxDatabasesPerInstance: u, MaxCPUCores: u, MaxMemoryMB: u, MaxDatabaseInstanceSizeMB: u, MaxStorageMB: u,
			MaxDatabaseCPUCores: u, MaxDatabaseMemoryMB: u,
			AllowCustomTLS: true, AllowPrivilegedHostMounts: true, AllowShellExec: true, AllowSharedStorage: true, AllowDNSProviders: true, AllowCustomLabels: true,
			AllowOfficialImageUser: true,
			AllowPlatformRunners:   true,
			AllowServiceRuntime:    true,
		},
	}
	if err := db.Create(&plans).Error; err != nil {
		return fmt.Errorf("seed plans: %w", err)
	}
	logger.Info("seeded default plans", "count", len(plans))
	return nil
}

func SeedDefaultOrganization(db *gorm.DB) (*models.Organization, error) {
	var org models.Organization
	err := db.Where("is_default = ?", true).First(&org).Error
	if err == nil {
		return &org, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	org = models.Organization{
		Name:        models.DefaultOrganizationName,
		DisplayName: models.DefaultOrganizationLabel,
		IsDefault:   true,
	}
	if err := db.Create(&org).Error; err != nil {
		return nil, fmt.Errorf("seed default organization: %w", err)
	}
	logger.Info("seeded default organization", "id", org.ID)
	return &org, nil
}
