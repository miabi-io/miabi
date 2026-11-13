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

package apply

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/robfig/cron/v3"
)

var (
	// ErrInstanceExists refuses taking over an existing instance without adopt: true.
	ErrInstanceExists = errors.New("INSTANCE_EXISTS")
	// ErrDatabaseNameTaken refuses an exact database name that another owner already uses on the instance.
	ErrDatabaseNameTaken = errors.New("DATABASE_NAME_TAKEN")
)

type BackupScheduleStore interface {
	ListSchedulesByDatabase(databaseID uint) ([]models.BackupSchedule, error)
	CreateSchedule(s *models.BackupSchedule) error
	UpdateSchedule(s *models.BackupSchedule) error
	DeleteSchedule(id uint) error
}

type SetScheduleStore interface {
	ListSchedulesByInstance(instanceID uint) ([]models.DatabaseBackupSetSchedule, error)
	CreateSchedule(s *models.DatabaseBackupSetSchedule) error
	UpdateSchedule(s *models.DatabaseBackupSetSchedule) error
	DeleteSchedule(id uint) error
}

// ScheduleRegistrar is the running cron; a schedule written without it would only start at the next restart.
type ScheduleRegistrar interface {
	Register(s models.BackupSchedule)
	Unregister(scheduleID uint)
	RegisterSet(s models.DatabaseBackupSetSchedule)
	UnregisterSet(scheduleID uint)
}

type BackupTarget interface {
	DatabaseDestination(workspaceID uint) (backup.Destination, error)
}

type Entitlements interface {
	RequireMutable(flag string) error
}

// BackupPolicies converges backup and recoveryPoints blocks; without it a declared block is refused, never dropped.
type BackupPolicies struct {
	Schedules    BackupScheduleStore
	Sets         SetScheduleStore
	Cron         ScheduleRegistrar
	Target       BackupTarget
	Entitlements Entitlements
}

func (s *Service) SetBackupPolicies(p BackupPolicies) { s.backups = &p }

// Presets hash the resource identity into a minute and a night hour, so thirty databases never dump at once.
func resolveSchedule(workspaceID uint, kind declarative.Kind, name, declared string) string {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%d/%s/%s", workspaceID, kind, name)
	sum := h.Sum32()
	minute, hour, day := sum%60, 1+(sum/60)%4, (sum/240)%7
	switch declared {
	case declarative.SchedulePresetHourly:
		return fmt.Sprintf("%d * * * *", minute)
	case declarative.SchedulePresetDaily:
		return fmt.Sprintf("%d %d * * *", minute, hour)
	case declarative.SchedulePresetWeekly:
		return fmt.Sprintf("%d %d * * %d", minute, hour, day)
	}
	return declared
}

func checkSchedule(kind declarative.Kind, name, field, declared string) error {
	if _, err := cron.ParseStandard(resolveSchedule(0, kind, name, declarative.ScheduleOrDefault(declared))); err != nil {
		return fmt.Errorf("%w: %s %q: %s.schedule %q: %v", ErrInvalidManifest, strings.ToLower(string(kind)), name, field, declared, err)
	}
	return nil
}

func ownedBy(inst *models.DatabaseInstance, kind declarative.Kind, name string) bool {
	return inst.Metadata[models.MetaDeclarativeKind] == string(kind) && inst.Metadata[models.MetaDeclarativeName] == name
}

func ownership(ctx context.Context, kind declarative.Kind, name string) models.Metadata {
	m := models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, managedBy(ctx),
		models.MetaDeclarativeKind, string(kind), models.MetaDeclarativeName, name)
	return tagSource(ctx, m)
}

func encrypted(d backup.Destination) bool { return d.GPGPassphrase != "" || d.AgeRecipients != "" }

// checkDatabaseDecls runs at plan time, so a refusal stops the bundle before any resource is touched.
func (s *Service) checkDatabaseDecls(ctx context.Context, workspaceID uint, plan *declarative.Plan, desired, actual *declarative.ResourceSet, ownerSrc string) error {
	instances, err := s.dbs.List(workspaceID)
	if err != nil {
		return err
	}
	live := make(map[string]*models.DatabaseInstance, len(instances))
	for i := range instances {
		live[instances[i].Name] = &instances[i]
	}
	adopted := map[string]bool{}
	for i := range plan.Changes {
		ch := &plan.Changes[i]
		if ch.Kind != declarative.KindDatabaseInstance {
			continue
		}
		if ch.Action == declarative.ActionDelete {
			if inst := live[ch.Name]; inst != nil && inst.Metadata[models.MetaAdopted] == "true" {
				ch.Reason = "release adopted instance; it and its data are kept"
			}
			continue
		}
		r, _ := desired.Get(string(ch.Kind) + "/" + ch.Name)
		spec := r.DatabaseInstance
		if ch.Action == declarative.ActionCreate {
			if inst := live[ch.Name]; inst != nil {
				if err := s.adoptRefusal(inst, spec, ch.Name); err != nil {
					return err
				}
				ch.Reason = "adopt existing instance"
				adopted[ch.Name] = true
			}
		} else if a, ok := actual.Get(string(ch.Kind) + "/" + ch.Name); ok {
			if src := a.Metadata.Labels[declarative.LabelGitOpsSource]; src != "" && ownerSrc != "" && src != ownerSrc {
				return fmt.Errorf("%w: databaseinstance %q is owned by another GitOps project; release it there first", ErrInvalidManifest, ch.Name)
			}
		}
		if ch.Action == declarative.ActionCreate || changesPrefix(ch, "recoveryPoints.") {
			if err := s.checkRecoveryPoints(workspaceID, ch.Name, spec.RecoveryPoints); err != nil {
				return err
			}
		}
	}
	if len(adopted) > 0 {
		// Before adoption the instance also reads as a legacy Database; adopting it is not removing it.
		kept := plan.Changes[:0]
		for _, ch := range plan.Changes {
			if ch.Kind == declarative.KindDatabase && ch.Action == declarative.ActionDelete && adopted[ch.Name] {
				continue
			}
			kept = append(kept, ch)
		}
		plan.Changes = kept
	}
	for _, ch := range plan.Changes {
		if ch.Kind != declarative.KindDatabase || (ch.Action != declarative.ActionCreate && ch.Action != declarative.ActionUpdate) {
			continue
		}
		r, _ := desired.Get(string(ch.Kind) + "/" + ch.Name)
		spec := r.Database
		if ch.Action == declarative.ActionCreate && spec.Instance.Name != "" {
			if err := s.checkNamedTarget(ch.Name, spec, live[spec.Instance.Name], desired); err != nil {
				return err
			}
		}
		if spec.Backup.On() {
			if s.backups == nil {
				return fmt.Errorf("%w: database %q: backup schedules are not available on this server", ErrInvalidManifest, ch.Name)
			}
			if err := checkSchedule(ch.Kind, ch.Name, "backup", spec.Backup.Schedule); err != nil {
				return err
			}
		}
	}
	plan.Warnings = append(plan.Warnings, doubleDumps(desired)...)
	return nil
}

// An unchanged block is not re-checked, so a lapsed license never blocks a sync that changes nothing.
func changesPrefix(ch *declarative.Change, prefix string) bool {
	for _, f := range ch.Fields {
		if strings.HasPrefix(f.Field, prefix) {
			return true
		}
	}
	return false
}

func (s *Service) adoptRefusal(inst *models.DatabaseInstance, spec *declarative.DatabaseInstanceSpec, name string) error {
	if !spec.Adopt {
		return fmt.Errorf("%w: %w: database instance %q already exists and no manifest owns it; set adopt: true to take it over",
			ErrInvalidManifest, ErrInstanceExists, name)
	}
	if k := inst.Metadata[models.MetaDeclarativeKind]; k != "" {
		return fmt.Errorf("%w: database instance %q is owned by %s %q and cannot be adopted",
			ErrInvalidManifest, name, k, inst.Metadata[models.MetaDeclarativeName])
	}
	switch owner := inst.Metadata[models.MetaManagedBy]; owner {
	case models.ManagedByGitOps, models.ManagedByMarketplace, ManagedByApply:
		return fmt.Errorf("%w: database instance %q is managed by %s and cannot be adopted", ErrInvalidManifest, name, owner)
	}
	if string(inst.Engine) != spec.Engine {
		return fmt.Errorf("%w: database instance %q runs %s, but the manifest declares %s", ErrInvalidManifest, name, inst.Engine, spec.Engine)
	}
	if spec.Version != "" && spec.Version != inst.Version {
		return fmt.Errorf("%w: database instance %q runs version %s, but the manifest declares %s; adopt it at %s, then upgrade",
			ErrInvalidManifest, name, inst.Version, spec.Version, inst.Version)
	}
	if loc := spec.Location(); loc != "" && loc != s.locationName(inst.ClusterID) {
		return fmt.Errorf("%w: database instance %q is in %q, not %q", ErrInvalidManifest, name, s.locationName(inst.ClusterID), loc)
	}
	if sc := spec.StorageClass; sc != "" && sc != inst.StorageClassName {
		return fmt.Errorf("%w: database instance %q is on storage class %q, not %q", ErrInvalidManifest, name, inst.StorageClassName, sc)
	}
	return nil
}

func (s *Service) checkRecoveryPoints(workspaceID uint, name string, rp *declarative.RecoveryPointsSpec) error {
	if !rp.On() {
		return nil
	}
	if s.backups == nil {
		return fmt.Errorf("%w: databaseinstance %q: recovery points are not available on this server", ErrInvalidManifest, name)
	}
	if ee := s.backups.Entitlements; ee != nil {
		if err := ee.RequireMutable(enterprise.FlagRecoveryPoints); err != nil {
			return fmt.Errorf("%w: databaseinstance %q: recoveryPoints need the %s Enterprise feature: %v",
				ErrInvalidManifest, name, enterprise.FlagRecoveryPoints, err)
		}
	}
	if err := checkSchedule(declarative.KindDatabaseInstance, name, "recoveryPoints", rp.Schedule); err != nil {
		return err
	}
	if s.backups.Target == nil {
		return nil
	}
	dest, err := s.backups.Target.DatabaseDestination(workspaceID)
	if err != nil {
		return err
	}
	if dest.Type != "s3" {
		return fmt.Errorf("%w: databaseinstance %q: recovery points need the workspace S3 backup target; configure it under Workspace settings → Backups",
			ErrInvalidManifest, name)
	}
	if rp.RequireEncryption && !encrypted(dest) {
		return fmt.Errorf("%w: databaseinstance %q: recoveryPoints.requireEncryption is set, but the workspace has no backup encryption configured",
			ErrInvalidManifest, name)
	}
	return nil
}

func (s *Service) checkNamedTarget(name string, spec *declarative.DatabaseSpec, inst *models.DatabaseInstance, desired *declarative.ResourceSet) error {
	target := spec.Instance.Name
	engine, version, location := "", "", ""
	switch {
	case inst != nil:
		engine, version, location = string(inst.Engine), inst.Version, s.locationName(inst.ClusterID)
		if spec.Instance.Type == "dedicated" && !ownedBy(inst, declarative.KindDatabase, name) {
			return fmt.Errorf("%w: database %q: instance %q already exists; a dedicated database provisions its own", ErrInvalidManifest, name, target)
		}
		if spec.Instance.Type != "dedicated" && database.IsDedicated(inst) {
			return fmt.Errorf("%w: database %q: instance %q is dedicated to a single dependency and hosts no other databases", ErrInvalidManifest, name, target)
		}
	default:
		di, ok := desired.Get(string(declarative.KindDatabaseInstance) + "/" + target)
		if !ok {
			if spec.Instance.Type == "shared" {
				return fmt.Errorf("%w: database %q: instance %q does not exist and is not declared in this apply", ErrInvalidManifest, name, target)
			}
			return nil
		}
		engine, version, location = di.DatabaseInstance.Engine, di.DatabaseInstance.Version, di.DatabaseInstance.Location()
	}
	if engine != spec.Engine {
		return fmt.Errorf("%w: database %q: instance %q runs %s, not %s", ErrInvalidManifest, name, target, engine, spec.Engine)
	}
	if spec.Version != "" && version != "" && spec.Version != version {
		return fmt.Errorf("%w: database %q: instance %q runs version %s, not %s", ErrInvalidManifest, name, target, version, spec.Version)
	}
	if loc := spec.Location(); loc != "" && location != "" && loc != location {
		return fmt.Errorf("%w: database %q: instance %q is in %q, not %q", ErrInvalidManifest, name, target, location, loc)
	}
	return nil
}

func doubleDumps(desired *declarative.ResourceSet) []string {
	var out []string
	for _, r := range desired.ByKind(declarative.KindDatabase) {
		if !r.Database.Backup.On() || r.Database.Instance.Name == "" {
			continue
		}
		di, ok := desired.Get(string(declarative.KindDatabaseInstance) + "/" + r.Database.Instance.Name)
		if ok && di.DatabaseInstance.RecoveryPoints.On() {
			out = append(out, fmt.Sprintf("database %q is backed up on its own and by the recovery points of instance %q, so it is dumped twice",
				r.Metadata.Name, r.Database.Instance.Name))
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) applyDatabaseInstance(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	switch ch.Action {
	case declarative.ActionCreate:
		spec := desired.DatabaseInstance
		if inst, err := s.findInstance(workspaceID, ch.Name); err == nil {
			if err := s.adoptRefusal(inst, spec, ch.Name); err != nil {
				return err
			}
			own := ownership(ctx, declarative.KindDatabaseInstance, ch.Name)
			own = models.SetBuiltin(own, models.MetaAdopted, "true")
			if inst, err = s.dbs.SetMetadata(workspaceID, inst.ID, own); err != nil {
				return err
			}
			if spec.Resources != nil {
				if err := s.resizeInstance(ctx, inst, spec.Resources, ch.Name); err != nil {
					return err
				}
			}
			return s.reconcileRecoveryPoints(workspaceID, inst, ch.Name, spec.RecoveryPoints)
		}
		target, err := s.place(ctx, workspaceID, ch, spec.Location(), false)
		if err != nil {
			return err
		}
		res := database.Resources{}
		if spec.Resources != nil {
			mb, _ := spec.Resources.MemoryBytes()
			nc, _ := spec.Resources.NanoCPUs()
			res = database.Resources{MemoryBytes: mb, NanoCPUs: nc, Size: spec.Resources.Size}
		}
		inst, err := s.dbs.Provision(ctx, workspaceID, target.ServerID, ch.Name, models.DBEngine(spec.Engine), spec.Version,
			0, spec.StorageClass, res, ownership(ctx, declarative.KindDatabaseInstance, ch.Name), nil)
		if err != nil {
			return err
		}
		if inst.Name != ch.Name {
			return fmt.Errorf("databaseinstance %q was created as %q because the name was taken meanwhile", ch.Name, inst.Name)
		}
		return s.reconcileRecoveryPoints(workspaceID, inst, ch.Name, spec.RecoveryPoints)
	case declarative.ActionUpdate:
		if err := refuseImmutable(ch); err != nil {
			return err
		}
		inst, err := s.findInstance(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		spec := desired.DatabaseInstance
		resize, schedule := false, false
		for _, f := range ch.Fields {
			switch {
			case f.Field == "engine":
				return fmt.Errorf("%w: databaseinstance %q runs %s; an engine cannot change. Delete it and apply again", ErrInvalidManifest, ch.Name, f.From)
			case f.Field == "version":
				plan, err := s.dbs.PlanUpgrade(workspaceID, inst.ID, f.To)
				if err != nil {
					return fmt.Errorf("databaseinstance %q: upgrade %s → %s: %w", ch.Name, f.From, f.To, err)
				}
				if _, err := s.dbs.Upgrade(ctx, workspaceID, inst.ID, f.To, plan.Major); err != nil {
					return fmt.Errorf("databaseinstance %q: %w", ch.Name, err)
				}
			case strings.HasPrefix(f.Field, "resources."):
				resize = true
			case strings.HasPrefix(f.Field, "recoveryPoints."):
				schedule = true
			}
		}
		if resize {
			if err := s.resizeInstance(ctx, inst, spec.Resources, ch.Name); err != nil {
				return err
			}
		}
		if schedule {
			return s.reconcileRecoveryPoints(workspaceID, inst, ch.Name, spec.RecoveryPoints)
		}
		return nil
	case declarative.ActionDelete:
		inst, err := s.findInstance(workspaceID, ch.Name)
		if err != nil || !ownedBy(inst, declarative.KindDatabaseInstance, ch.Name) {
			return nil
		}
		if inst.Metadata[models.MetaAdopted] == "true" {
			if err := s.dropManagedSetSchedules(inst.ID); err != nil {
				return err
			}
			_, err := s.dbs.SetMetadata(workspaceID, inst.ID, map[string]string{models.MetaManagedBy: models.ManagedByUser},
				models.MetaDeclarativeKind, models.MetaDeclarativeName, models.MetaAdopted, models.MetaGitOpsSource)
			return err
		}
		dbs, _ := s.dbs.ListDatabases(workspaceID, inst.ID)
		if len(dbs) > 0 {
			names := make([]string, 0, len(dbs))
			for i := range dbs {
				names = append(names, dbs[i].Name)
			}
			return fmt.Errorf("databaseinstance %q still holds databases (%s); remove them first", ch.Name, strings.Join(names, ", "))
		}
		if err := s.dropManagedSetSchedules(inst.ID); err != nil {
			return err
		}
		return s.deleteInstance(ctx, inst)
	}
	return nil
}

// Plain numbers left unstated keep the instance's; a named size carries both limits.
func (s *Service) resizeInstance(ctx context.Context, inst *models.DatabaseInstance, spec *declarative.DatabaseResourcesSpec, name string) error {
	if spec == nil {
		return nil
	}
	mb, _ := spec.MemoryBytes()
	nc, _ := spec.NanoCPUs()
	res := database.Resources{MemoryBytes: mb, NanoCPUs: nc, Size: spec.Size}
	if res.Size == "" {
		if spec.Memory == "" {
			res.MemoryBytes = inst.MemoryBytes
		}
		if spec.CPU == "" {
			res.NanoCPUs = inst.NanoCPUs
		}
	}
	if _, err := s.dbs.Resize(ctx, inst, res); err != nil {
		return fmt.Errorf("%q: %w", name, err)
	}
	return nil
}

func (s *Service) createOnNamedInstance(ctx context.Context, workspaceID uint, ch declarative.Change, spec *declarative.DatabaseSpec, meta models.Metadata) (*models.Database, error) {
	target := spec.Instance.Name
	inst, err := s.findInstance(workspaceID, target)
	if err != nil {
		if spec.Instance.Type == "shared" {
			return nil, fmt.Errorf("database %q: instance %q does not exist", ch.Name, target)
		}
		place, err := s.place(ctx, workspaceID, ch, spec.Location(), false)
		if err != nil {
			return nil, err
		}
		own := models.SetBuiltin(models.Metadata{}, models.MetaDeclarativeKind, string(declarative.KindDatabase), models.MetaDeclarativeName, ch.Name)
		for k, v := range meta {
			own = models.SetBuiltin(own, k, v)
		}
		if spec.Instance.Type == "dedicated" {
			own = models.SetBuiltin(own, models.MetaPlacement, models.PlacementValueDedicated)
		}
		inst, err = s.dbs.Provision(ctx, workspaceID, place.ServerID, target, models.DBEngine(spec.Engine), spec.Version, 0, "", database.Resources{}, own, nil)
		if err != nil {
			return nil, err
		}
		if inst.Name != target {
			return nil, fmt.Errorf("database %q: instance %q was created as %q because the name was taken meanwhile", ch.Name, target, inst.Name)
		}
	}
	db, err := s.dbs.EnsureNamedDatabase(ctx, workspaceID, inst, ch.Name, meta)
	if errors.Is(err, database.ErrNameTaken) {
		return nil, fmt.Errorf("%w: database %q: instance %q already has a database named %q that this manifest did not create",
			ErrDatabaseNameTaken, ch.Name, target, database.SanitizedName(ch.Name))
	}
	return db, err
}

// The instance-as-database engines keep their one row on the instance, so a legacy name resolves to it.
func (s *Service) declaredDatabase(workspaceID uint, name string) (*models.Database, error) {
	if db, _, ok := s.dbs.FindDatabaseByDeclName(workspaceID, name); ok {
		return db, nil
	}
	inst, err := s.findInstance(workspaceID, name)
	if err != nil {
		return nil, err
	}
	dbs, err := s.dbs.ListDatabases(workspaceID, inst.ID)
	if err != nil {
		return nil, err
	}
	if len(dbs) != 1 {
		return nil, fmt.Errorf("database %q has no single logical database to back up", name)
	}
	return &dbs[0], nil
}

func (s *Service) reconcileBackup(ctx context.Context, workspaceID uint, name string, spec *declarative.BackupScheduleSpec) error {
	if spec == nil || s.backups == nil || s.backups.Schedules == nil {
		return nil
	}
	db, err := s.declaredDatabase(workspaceID, name)
	if err != nil {
		return err
	}
	list, err := s.backups.Schedules.ListSchedulesByDatabase(db.ID)
	if err != nil {
		return err
	}
	var managed []models.BackupSchedule
	for _, sc := range list {
		if sc.ManagedBy != "" {
			managed = append(managed, sc)
		}
	}
	if !spec.On() {
		return s.dropBackupSchedules(managed)
	}
	declared := declarative.ScheduleOrDefault(spec.Schedule)
	sc := models.BackupSchedule{WorkspaceID: workspaceID, DatabaseID: db.ID, Destination: "workspace"}
	if len(managed) > 0 {
		sc = managed[0]
		if err := s.dropBackupSchedules(managed[1:]); err != nil {
			return err
		}
	}
	sc.Cron = resolveSchedule(workspaceID, declarative.KindDatabase, name, declared)
	sc.Declared, sc.Enabled, sc.ManagedBy = declared, true, managedBy(ctx)
	sc.MaxBackups, sc.RetentionDays = spec.Retention.KeepOr0(), spec.Retention.DaysOr0()
	if sc.ID == 0 {
		err = s.backups.Schedules.CreateSchedule(&sc)
	} else {
		err = s.backups.Schedules.UpdateSchedule(&sc)
	}
	if err != nil {
		return err
	}
	if s.backups.Cron != nil {
		s.backups.Cron.Register(sc)
	}
	return nil
}

func (s *Service) dropBackupSchedules(list []models.BackupSchedule) error {
	for _, sc := range list {
		if s.backups.Cron != nil {
			s.backups.Cron.Unregister(sc.ID)
		}
		if err := s.backups.Schedules.DeleteSchedule(sc.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) dropManagedBackupSchedules(databaseID uint) error {
	if s.backups == nil || s.backups.Schedules == nil {
		return nil
	}
	list, err := s.backups.Schedules.ListSchedulesByDatabase(databaseID)
	if err != nil {
		return err
	}
	var managed []models.BackupSchedule
	for _, sc := range list {
		if sc.ManagedBy != "" {
			managed = append(managed, sc)
		}
	}
	return s.dropBackupSchedules(managed)
}

func (s *Service) reconcileRecoveryPoints(workspaceID uint, inst *models.DatabaseInstance, name string, spec *declarative.RecoveryPointsSpec) error {
	if spec == nil || s.backups == nil || s.backups.Sets == nil {
		return nil
	}
	list, err := s.backups.Sets.ListSchedulesByInstance(inst.ID)
	if err != nil {
		return err
	}
	var managed []models.DatabaseBackupSetSchedule
	for _, sc := range list {
		if sc.ManagedBy != "" {
			managed = append(managed, sc)
		}
	}
	if !spec.On() {
		return s.dropSetSchedules(managed)
	}
	declared := declarative.ScheduleOrDefault(spec.Schedule)
	sc := models.DatabaseBackupSetSchedule{WorkspaceID: workspaceID, InstanceID: inst.ID}
	if len(managed) > 0 {
		sc = managed[0]
		if err := s.dropSetSchedules(managed[1:]); err != nil {
			return err
		}
	}
	sc.Cron = resolveSchedule(workspaceID, declarative.KindDatabaseInstance, name, declared)
	sc.Declared, sc.Enabled = declared, true
	sc.ManagedBy = inst.Metadata[models.MetaManagedBy]
	if sc.ManagedBy == "" {
		sc.ManagedBy = ManagedByApply
	}
	sc.MaxSets, sc.RetentionDays = spec.Retention.KeepOr0(), spec.Retention.DaysOr0()
	sc.Concurrency, sc.RequireEncryption = spec.Concurrency, spec.RequireEncryption
	if sc.ID == 0 {
		err = s.backups.Sets.CreateSchedule(&sc)
	} else {
		err = s.backups.Sets.UpdateSchedule(&sc)
	}
	if err != nil {
		return err
	}
	if s.backups.Cron != nil {
		s.backups.Cron.RegisterSet(sc)
	}
	return nil
}

func (s *Service) dropSetSchedules(list []models.DatabaseBackupSetSchedule) error {
	for _, sc := range list {
		if s.backups.Cron != nil {
			s.backups.Cron.UnregisterSet(sc.ID)
		}
		if err := s.backups.Sets.DeleteSchedule(sc.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) dropManagedSetSchedules(instanceID uint) error {
	if s.backups == nil || s.backups.Sets == nil {
		return nil
	}
	list, err := s.backups.Sets.ListSchedulesByInstance(instanceID)
	if err != nil {
		return err
	}
	var managed []models.DatabaseBackupSetSchedule
	for _, sc := range list {
		if sc.ManagedBy != "" {
			managed = append(managed, sc)
		}
	}
	return s.dropSetSchedules(managed)
}

// A live database with no declared schedule reads as disabled, which an omitted block never compares against.
func (s *Service) backupSpecOf(databaseID uint) *declarative.BackupScheduleSpec {
	off := false
	out := &declarative.BackupScheduleSpec{Enabled: &off}
	if s.backups == nil || s.backups.Schedules == nil || databaseID == 0 {
		return out
	}
	list, err := s.backups.Schedules.ListSchedulesByDatabase(databaseID)
	if err != nil {
		return out
	}
	for _, sc := range list {
		if sc.ManagedBy == "" {
			continue
		}
		on := true
		return &declarative.BackupScheduleSpec{Enabled: &on, Schedule: sc.Declared,
			Retention: &declarative.RetentionSpec{Keep: sc.MaxBackups, Days: sc.RetentionDays}}
	}
	return out
}

func (s *Service) recoveryPointsSpecOf(instanceID uint) *declarative.RecoveryPointsSpec {
	off := false
	out := &declarative.RecoveryPointsSpec{Enabled: &off}
	if s.backups == nil || s.backups.Sets == nil {
		return out
	}
	list, err := s.backups.Sets.ListSchedulesByInstance(instanceID)
	if err != nil {
		return out
	}
	for _, sc := range list {
		if sc.ManagedBy == "" {
			continue
		}
		on := true
		return &declarative.RecoveryPointsSpec{Enabled: &on, Schedule: sc.Declared,
			Retention:   &declarative.RetentionSpec{Keep: sc.MaxSets, Days: sc.RetentionDays},
			Concurrency: sc.Concurrency, RequireEncryption: sc.RequireEncryption}
	}
	return out
}
