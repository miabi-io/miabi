// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package migration

import (
	"fmt"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// Run executes all schema migrations via GORM AutoMigrate.
func Run(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&models.UpgradeStep{},
		&models.UpdateStatus{},
		&models.Server{},
		&models.GPUDevice{},
		&models.User{},
		&models.Session{},
		&models.APIKey{},
		&models.Workspace{},
		&models.WorkspaceMember{},
		&models.WorkspaceInvitation{},
		&models.PasswordResetToken{},
		&models.TwoFactorRecoveryCode{},
		&models.AuditLog{},
		&models.Registry{},
		&models.GitRepository{},
		&models.Network{},
		&models.NetworkAllocation{},
		&models.Stack{},
		&models.StackEnvVar{},
		&models.Application{},
		&models.AppEnvVar{},
		&models.AppPort{},
		&models.PortBinding{},
		&models.Deployment{},
		&models.Release{},
		&models.AppEvent{},
		&models.Route{},
		&models.Middleware{},
		&models.DatabaseInstance{},
		&models.Database{},
		&models.Volume{},
		&models.Backup{},
		&models.BackupSchedule{},
		&models.WorkspaceBackupSettings{},
		&models.VolumeBackup{},
		&models.PlatformBackup{},
		&models.PlatformBackupSettings{},
		&models.RegistrySettings{},
		&models.MetricSample{},
		&models.Setting{},
		&models.OAuthProvider{},
		&models.Webhook{},
		&models.WebhookDelivery{},
		&models.NotificationChannel{},
		&models.Job{},
		&models.CronJob{},
		&models.Secret{},
		&models.Certificate{},
		&models.Plan{},
		&models.WorkspaceQuota{},
		&models.License{},
		&models.Organization{},
		&models.SAMLConfig{},
		&models.SCIMToken{},
		&models.LDAPConfig{},
		&models.LDAPGroupMapping{},
		&models.CustomRole{},
		&models.ResourcePolicy{},
		&models.SIEMConfig{},
		&models.TemplateSource{},
		&models.Template{},
		&models.TemplateInstall{},
		&models.Domain{},
		&models.DNSProvider{},
		&models.DNSRecord{},
		&models.ACMEAccount{},
		&models.WorkspaceKey{},
		// GitOps & CI/CD
		&models.GitSource{},
		&models.Environment{},
		&models.ReleaseApproval{},
		&models.Image{},
		&models.PipelineDefinition{},
		&models.PipelineRun{},
		&models.PipelineStepRun{},
		&models.Runner{},
		&models.RunnerLease{},
		&models.AnalyticsRollup{},
		&models.Alert{},
		&models.Notification{},
	); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	// Partial unique index enforcing at most one ACTIVE lease per job at the DB
	// level (GORM can't express a partial index via struct tags). Keyed by
	// (kind, run_id) so pipeline-run and deploy-build id spaces never collide.
	if err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_runner_lease_active_run ` +
			`ON runner_leases (kind, run_id) WHERE status = 'active'`,
	).Error; err != nil {
		return fmt.Errorf("failed to create runner-lease active-run index: %w", err)
	}

	// Partial unique index: a hostname may be VERIFIED by at most one workspace
	// platform-wide, so two tenants can't both serve the same domain. Registration
	// (unverified rows) stays open; only verification is exclusive. Idempotent.
	if err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_domain_verified_name ` +
			`ON domains (lower(name)) WHERE verified`,
	).Error; err != nil {
		return fmt.Errorf("failed to create verified-domain uniqueness index: %w", err)
	}

	// Partial unique index: at most one ACTIVE alert per (workspace, dedup_key), so
	// a repeated signal folds into the existing alert instead of creating a new
	// row. Resolved/archived alerts stay as history and don't block a re-fire.
	if err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_alert_active_dedup ` +
			`ON alerts (workspace_id, dedup_key) WHERE state IN ('firing', 'acknowledged')`,
	).Error; err != nil {
		return fmt.Errorf("failed to create active-alert uniqueness index: %w", err)
	}

	logger.Info("database migrations applied")
	return nil
}
