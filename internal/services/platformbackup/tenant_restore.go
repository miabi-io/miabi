// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
)

// TenantRestoreReport is the outcome of restoring workload data.
type TenantRestoreReport struct {
	Ref               string   `json:"ref"`
	DatabasesRestored int      `json:"databases_restored"`
	VolumesRestored   int      `json:"volumes_restored"`
	Skipped           []string `json:"skipped"`
	Failures          []string `json:"failures"`
}

// RestoreTenantData restores a recovery point's workload data — every tenant
// database dump and volume archive it carries.
//
// It runs LATE, after the control plane is back and reconcile has recreated the
// database containers and volumes. That ordering is not a preference: a dump has
// nowhere to go until its database instance exists, and the credentials to reach
// that instance come from the control-plane rows the earlier phase restored.
//
// Individual failures are collected rather than returned. An operator recovering
// forty workspaces needs to know which three did not come back, not to be
// stopped at the first one.
func (s *Service) RestoreTenantData(ctx context.Context, set *models.PlatformBackupSet) (*TenantRestoreReport, error) {
	rep := &TenantRestoreReport{Ref: set.Ref, Skipped: []string{}, Failures: []string{}}
	if s.tenants == nil {
		return rep, errors.New("tenant restore is unavailable: no tenant source is wired")
	}
	st, err := s.getSettings()
	if err != nil {
		return rep, err
	}
	cfg, err := s.s3Config(st)
	if err != nil {
		return rep, err
	}
	if cfg == nil {
		return rep, ErrS3NotConfigured
	}

	// Resolve the live tenant graph once. Artifacts are matched against it by
	// natural key (workspace slug + database name / volume name), because the
	// numeric ids in a recovery point belong to the install that produced it.
	dbs, err := s.tenants.ListTenantDatabases()
	if err != nil {
		return rep, fmt.Errorf("list tenant databases: %w", err)
	}
	byDatabase := make(map[string]TenantDatabase, len(dbs))
	for _, td := range dbs {
		byDatabase[tenantKey(td.Workspace, td.Database.Name)] = td
	}

	for i := range set.Items {
		item := &set.Items[i]
		if item.Status != models.BackupCompleted || item.Filename == "" {
			continue
		}
		switch item.Subject {
		case models.PlatformBackupTenantDatabase:
			td, ok := byDatabase[tenantKey(item.WorkspaceSlug, item.DatabaseName)]
			if !ok {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf(
					"database %s/%s — no matching database exists on this platform yet; recreate it and restore again",
					item.WorkspaceSlug, item.DatabaseName))
				continue
			}
			if err := s.restoreTenantDatabase(ctx, st, cfg, td, item); err != nil {
				rep.Failures = append(rep.Failures, fmt.Sprintf("database %s/%s: %v", item.WorkspaceSlug, item.DatabaseName, err))
				continue
			}
			rep.DatabasesRestored++

		case models.PlatformBackupTenantVolume:
			if err := s.restoreTenantVolume(ctx, st, cfg, item); err != nil {
				rep.Failures = append(rep.Failures, fmt.Sprintf("volume %s: %v", item.VolumeName, err))
				continue
			}
			rep.VolumesRestored++
		}
	}

	logger.Info("tenant data restore finished", "ref", set.Ref,
		"databases", rep.DatabasesRestored, "volumes", rep.VolumesRestored,
		"skipped", len(rep.Skipped), "failures", len(rep.Failures))
	return rep, nil
}

func (s *Service) restoreTenantDatabase(ctx context.Context, st *models.PlatformBackupSettings, cfg *backup.S3Config, td TenantDatabase, item *models.PlatformBackup) error {
	filename, err := s.resolveArtifactObject(ctx, st, item)
	if err != nil {
		return err
	}
	dest := backup.Destination{Type: destS3, S3: withPath(cfg, item.S3Path)}
	if item.Encrypted || strings.HasSuffix(item.Filename, ".gpg") {
		pass, err := s.passphrase(st)
		if err != nil {
			return err
		}
		if pass == "" {
			return fmt.Errorf("this artifact is encrypted but no backup passphrase is set: %w", ErrNoPassphrase)
		}
		dest.GPGPassphrase = pass
	}
	return s.tenants.RestoreTenantDatabase(ctx, td, dest, filename)
}

// resolveArtifactObject returns the object name that is actually in the bucket.
//
// Recovery points taken before the artifact-naming fix recorded the plain name
// while the tool uploaded the encrypted one, so the row points at a key that was
// never written. The object is there under "<name>.gpg"; finding it costs one
// HEAD request and turns an unusable recovery point into a usable one. New
// recovery points record the right name and take the first branch.
func (s *Service) resolveArtifactObject(ctx context.Context, st *models.PlatformBackupSettings, item *models.PlatformBackup) (string, error) {
	store, err := s.blobStore(st)
	if err != nil {
		return item.Filename, nil // cannot check; use what was recorded
	}
	if found, err := store.Exists(ctx, objectKey(item)); err == nil && found {
		return item.Filename, nil
	}
	if strings.HasSuffix(item.Filename, ".gpg") {
		return item.Filename, nil
	}
	alt := *item
	alt.Filename = item.Filename + ".gpg"
	if found, err := store.Exists(ctx, objectKey(&alt)); err == nil && found {
		logger.Warn("artifact recorded under its pre-encryption name; using the encrypted object that was actually uploaded",
			"recorded", item.Filename, "found", alt.Filename)
		return alt.Filename, nil
	}
	return item.Filename, nil
}

// restoreTenantVolume unpacks a workspace volume archive back into its volume,
// creating the volume when reconcile has not already done so.
func (s *Service) restoreTenantVolume(ctx context.Context, st *models.PlatformBackupSettings, cfg *backup.S3Config, item *models.PlatformBackup) error {
	if item.VolumeName == "" {
		return errors.New("artifact has no target volume")
	}
	dc, err := s.docker()
	if err != nil {
		return err
	}
	if _, err := dc.CreateVolume(ctx, item.VolumeName, map[string]string{
		docker.LabelManaged:   "true",
		docker.LabelWorkspace: fmt.Sprint(item.WorkspaceID),
	}, 0); err != nil {
		return fmt.Errorf("create volume: %w", err)
	}

	filename, err := s.resolveArtifactObject(ctx, st, item)
	if err != nil {
		return err
	}
	env := backup.S3Env(cfg)
	if item.Encrypted || strings.HasSuffix(filename, ".gpg") {
		pass, err := s.passphrase(st)
		if err != nil {
			return err
		}
		if pass == "" {
			return fmt.Errorf("this artifact is encrypted but no backup passphrase is set: %w", ErrNoPassphrase)
		}
		env = append(env, "GPG_PASSPHRASE="+pass)
	}

	image := s.volImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	nets, err := s.backupNetworks(ctx, dc)
	if err != nil {
		return err
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:     fmt.Sprintf("mb-tenant-volrestore-%d", item.ID),
		Image:    image,
		Env:      env,
		Cmd:      []string{"restore", "--storage", "s3", "--remote-path", item.S3Path, "--file", filename},
		Mounts:   map[string]string{item.VolumeName: volumeMount},
		Networks: nets,
		Labels:   map[string]string{docker.LabelManaged: "true"},
	})
	if err != nil || exit != 0 {
		return fmt.Errorf("volume restore exited with code %d: %s", exit, out)
	}
	return nil
}

// tenantKey identifies a tenant database across installs: the workspace slug and
// the database name, never the numeric ids, which are meaningless on the host
// that is doing the recovering.
func tenantKey(workspace, database string) string {
	return slugish(workspace) + "/" + strings.ToLower(strings.TrimSpace(database))
}

// LatestRestorableSet returns the newest completed recovery point, which is what
// a post-restore reconcile pulls tenant data from.
func (s *Service) LatestRestorableSet() (*models.PlatformBackupSet, error) {
	sets, err := s.sets.List()
	if err != nil {
		return nil, err
	}
	for i := range sets {
		if sets[i].Status == models.BackupCompleted {
			return s.sets.FindByID(sets[i].ID)
		}
	}
	return nil, nil
}
