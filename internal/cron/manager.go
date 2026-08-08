// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cron is a generic recurring-task scheduler over robfig/cron. Tasks are keyed by a
// string ("backup:12", "cronjob:34") so different sources share one loop and one status view
// without id collisions. Schedules load at startup and can be (de)registered at runtime.
package cron

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/robfig/cron/v3"
)

// JobStatus is a point-in-time snapshot of a scheduled task for the admin UI.
type JobStatus struct {
	Kind      string     `json:"kind"` // backup | cronjob
	ID        uint       `json:"id"`   // source-specific id (schedule / cronjob)
	Name      string     `json:"name"`
	Schedule  string     `json:"schedule"`
	Running   bool       `json:"running"`
	LastRunAt *time.Time `json:"last_run_at"`
	LastError string     `json:"last_error,omitempty"`
	NextRunAt *time.Time `json:"next_run_at"`
}

type jobState struct {
	kind      string
	id        uint
	name      string
	schedule  string
	running   bool
	lastRunAt *time.Time
	lastError string
}

type Manager struct {
	c        *cron.Cron
	backups  *backup.Service
	dbs      *repositories.DatabaseRepository
	sched    *repositories.BackupRepository
	settings *backupsettings.Service

	mu      sync.Mutex
	entries map[string]cron.EntryID
	state   map[string]*jobState
}

func NewManager(backups *backup.Service, dbs *repositories.DatabaseRepository, sched *repositories.BackupRepository, settings *backupsettings.Service) *Manager {
	return &Manager{
		// SkipIfStillRunning drops a tick whose previous invocation of the same task
		// is still running, so a job slower than its interval (e.g. a large backup)
		// never overlaps itself on the same schedule/database.
		c:        cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger))),
		backups:  backups,
		dbs:      dbs,
		sched:    sched,
		settings: settings,
		entries:  make(map[string]cron.EntryID),
		state:    make(map[string]*jobState),
	}
}

// Snapshot returns the current status of every registered task.
func (m *Manager) Snapshot() []JobStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]JobStatus, 0, len(m.state))
	for key, st := range m.state {
		js := JobStatus{
			Kind:      st.kind,
			ID:        st.id,
			Name:      st.name,
			Schedule:  st.schedule,
			Running:   st.running,
			LastRunAt: st.lastRunAt,
			LastError: st.lastError,
		}
		if entryID, ok := m.entries[key]; ok {
			if next := m.c.Entry(entryID).Next; !next.IsZero() {
				n := next
				js.NextRunAt = &n
			}
		}
		out = append(out, js)
	}
	return out
}

// RegisterTask adds (or replaces) a task in the running cron, keyed by kind:id.
// fn runs on each tick; its error is surfaced in the snapshot's LastError.
func (m *Manager) RegisterTask(kind string, id uint, name, schedule string, fn func() error) error {
	key := taskKey(kind, id)
	m.UnregisterTask(kind, id)
	entryID, err := m.c.AddFunc(schedule, func() { m.run(key, fn) })
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.entries[key] = entryID
	m.state[key] = &jobState{kind: kind, id: id, name: name, schedule: schedule}
	m.mu.Unlock()
	return nil
}

// UnregisterTask removes a task from the running cron.
func (m *Manager) UnregisterTask(kind string, id uint) {
	key := taskKey(kind, id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if entryID, ok := m.entries[key]; ok {
		m.c.Remove(entryID)
		delete(m.entries, key)
	}
	delete(m.state, key)
}

func taskKey(kind string, id uint) string { return fmt.Sprintf("%s:%d", kind, id) }

// ValidateSpec reports whether spec is a valid standard (5-field) cron
// expression, so callers can reject a bad schedule before persisting it.
func ValidateSpec(spec string) error {
	_, err := cron.ParseStandard(spec)
	return err
}

func (m *Manager) run(key string, fn func() error) {
	m.setRunning(key, true)
	err := fn()
	if err != nil {
		logger.Error("scheduled task failed", "task", key, "error", err)
	}
	m.finish(key, err)
}

func (m *Manager) setRunning(key string, running bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.state[key]; ok {
		st.running = running
	}
}

func (m *Manager) finish(key string, runErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[key]
	if !ok {
		return
	}
	now := time.Now()
	st.running = false
	st.lastRunAt = &now
	if runErr != nil {
		st.lastError = runErr.Error()
	} else {
		st.lastError = ""
	}
}

// Start loads all enabled schedules and begins the cron loop.
func (m *Manager) Start() {
	schedules, err := m.sched.ListEnabledSchedules()
	if err != nil {
		logger.Error("failed to load backup schedules", "error", err)
	}
	for _, s := range schedules {
		m.Register(s)
	}
	m.c.Start()
	logger.Info("scheduler started", "backup_schedules", len(schedules))
}

// Stop halts the cron loop.
func (m *Manager) Stop() {
	if m.c != nil {
		m.c.Stop()
	}
}

// Register adds (or replaces) a backup schedule in the running cron.
func (m *Manager) Register(s models.BackupSchedule) {
	id, ws, dbID := s.ID, s.WorkspaceID, s.DatabaseID
	name := fmt.Sprintf("Backup: database #%d", s.DatabaseID)
	if err := m.RegisterTask("backup", id, name, s.Cron, func() error { return m.runBackup(id, ws, dbID) }); err != nil {
		logger.Error("invalid backup cron", "schedule", s.ID, "cron", s.Cron, "error", err)
	}
}

// Unregister removes a backup schedule from the running cron.
func (m *Manager) Unregister(scheduleID uint) { m.UnregisterTask("backup", scheduleID) }

func (m *Manager) runBackup(scheduleID, workspaceID, databaseID uint) error {
	db, err := m.dbs.FindDatabaseInWorkspace(workspaceID, databaseID)
	if err != nil {
		return fmt.Errorf("database not found: %w", err)
	}
	inst, err := m.dbs.FindByID(db.InstanceID)
	if err != nil {
		return fmt.Errorf("instance not found: %w", err)
	}
	sched, err := m.sched.FindScheduleInWorkspace(workspaceID, scheduleID)
	if err != nil {
		return fmt.Errorf("schedule not found: %w", err)
	}
	if _, err := m.backups.Run(context.Background(), inst, db, "scheduled", m.destinationFor(workspaceID)); err != nil {
		return err
	}
	now := time.Now()
	sched.LastRunAt = &now
	_ = m.sched.UpdateSchedule(sched)
	// Enforce retention for this database after a successful run.
	if sched.MaxBackups > 0 || sched.RetentionDays > 0 {
		_, _ = m.backups.Prune(context.Background(), databaseID, sched.MaxBackups, sched.RetentionDays)
	}
	return nil
}

// destinationFor resolves a scheduled backup's destination from the workspace's
// backup settings: the centralized S3 target (with the database path prefix)
// when configured, else local. Schedules no longer carry their own S3 config.
func (m *Manager) destinationFor(workspaceID uint) backup.Destination {
	if m.settings != nil {
		if cfg, path, err := m.settings.DatabaseBackupTarget(workspaceID); err == nil && cfg != nil {
			cfg.Path = path
			return backup.Destination{Type: "s3", S3: cfg}
		}
	}
	return backup.Destination{Type: "local"}
}
