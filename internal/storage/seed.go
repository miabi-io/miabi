// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// EnsureInstallID returns this instance's stable Install ID, generating and
// persisting one on first call. The ID is immutable for the life of the
// deployment: it uniquely identifies the instance to the license/customer portal
// (a customer copies it when purchasing a license), so it must survive restarts
// and never change. Idempotent and race-safe across concurrent boots
// (server + worker) via FirstOrCreate.
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

// SchemaVersion returns the version of the most recently applied upgrade step,
// or "" when none has run. A platform recovery point records it so restore can
// refuse to load a dump into an older binary than the one that produced it —
// a downgrade restore corrupts silently, where a refusal costs nothing.
func SchemaVersion(db *gorm.DB) string {
	var step models.UpgradeStep
	if err := db.Order("applied_at DESC, id DESC").First(&step).Error; err != nil {
		return ""
	}
	return step.Version
}

// SeedAdmin ensures a platform admin exists: returns the existing admin if one
// is already present, otherwise creates one from the configured credentials.
// Idempotent — safe to run on every boot. (Mirrors the Posta admin seeder.)
func SeedAdmin(db *gorm.DB, email, password string) (*models.User, error) {
	// Already bootstrapped: return the first admin (its workspace is then ensured).
	var existing models.User
	err := db.Where("role = ?", models.SystemRoleAdmin).Order("id ASC").First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return nil, fmt.Errorf("admin email and password are required to seed the first account")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash admin password: %w", err)
	}
	now := time.Now()
	admin := &models.User{
		Name:            "Admin",
		Username:        "admin",
		Email:           email,
		PasswordHash:    string(hash),
		Role:            models.SystemRoleAdmin,
		Active:          true,
		EmailVerifiedAt: &now,
	}
	if err := db.Create(admin).Error; err != nil {
		return nil, fmt.Errorf("create admin user: %w", err)
	}
	logger.Info("seeded platform admin", "email", email)
	return admin, nil
}

// SeedPlans creates the built-in plan catalog on first boot if no plans exist.
// Idempotent — a no-op once any plan is present (so admin edits are preserved).
// Limits use -1 for unlimited, 0 for none. "Free" is the default plan.
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
			Name: "Free", Description: "Starter limits for small workloads.", IsDefault: true, IsActive: true,
			MaxApps: 3, MaxDatabaseInstances: 1, MaxCronJobs: 2, MaxVolumes: 3, MaxNetworks: 1, MaxAPIKeys: 2, MaxMembers: 3,
			MaxDatabasesPerInstance: 2, MaxCPUCores: 2, MaxMemoryMB: 2048,
			MaxDatabaseInstanceSizeMB: 2048, MaxStorageMB: 10240,
			AllowCustomTLS: false, AllowPrivilegedHostMounts: false, AllowShellExec: false, AllowSharedStorage: false, AllowDNSProviders: false, AllowCustomLabels: false,
		},
		{
			Name: "Pro", Description: "Higher limits and custom TLS for production.", IsActive: true,
			MaxApps: 25, MaxDatabaseInstances: 10, MaxCronJobs: 50, MaxVolumes: 50, MaxNetworks: 10, MaxAPIKeys: 25, MaxMembers: 25,
			MaxDatabasesPerInstance: 20, MaxCPUCores: 16, MaxMemoryMB: 32768,
			MaxDatabaseInstanceSizeMB: 51200, MaxStorageMB: 512000,
			AllowCustomTLS: true, AllowPrivilegedHostMounts: false, AllowShellExec: true, AllowSharedStorage: true, AllowDNSProviders: true, AllowCustomLabels: true,
			AllowOfficialImageUser: true,
		},
		{
			Name: models.UnlimitedPlanName, Description: "No resource limits; all capabilities.", IsActive: true,
			MaxApps: u, MaxDatabaseInstances: u, MaxCronJobs: u, MaxVolumes: u, MaxNetworks: u, MaxAPIKeys: u, MaxMembers: u,
			MaxDatabasesPerInstance: u, MaxCPUCores: u, MaxMemoryMB: u, MaxDatabaseInstanceSizeMB: u, MaxStorageMB: u,
			AllowCustomTLS: true, AllowPrivilegedHostMounts: true, AllowShellExec: true, AllowSharedStorage: true, AllowDNSProviders: true, AllowCustomLabels: true,
			AllowOfficialImageUser: true,
		},
	}
	if err := db.Create(&plans).Error; err != nil {
		return fmt.Errorf("seed plans: %w", err)
	}
	logger.Info("seeded default plans", "count", len(plans))
	return nil
}

// SeedDefaultOrganization ensures exactly one default organization exists — the
// realm new workspaces and SSO providers attach to. Idempotent; safe on every
// boot. Returns the default org.
func SeedDefaultOrganization(db *gorm.DB) (*models.Organization, error) {
	var org models.Organization
	err := db.Where("is_default = ?", true).First(&org).Error
	if err == nil {
		return &org, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	org = models.Organization{Name: models.DefaultOrganizationName, DisplayName: "Default", IsDefault: true}
	if err := db.Create(&org).Error; err != nil {
		return nil, fmt.Errorf("seed default organization: %w", err)
	}
	logger.Info("seeded default organization", "id", org.ID)
	return &org, nil
}
