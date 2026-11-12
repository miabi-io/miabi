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

package handlers

import (
	"errors"
	"github.com/miabi-io/miabi/internal/services/storageclass"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/datavolume"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/placement"
	"github.com/miabi-io/miabi/internal/services/portforward"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type DatabaseHandler struct {
	svc     *database.Service
	apps    *application.Service
	forward *portforward.Service
	secrets *secret.Service
	users   *repositories.UserRepository
	audit   *audit.Logger
	cluster ClusterCap
	placer  *Placer
}

// SetPlacer wires location placement for database creates.
func (h *DatabaseHandler) SetPlacer(p *Placer) { h.placer = p }

// cluster may be nil, in which case the co-location guards always apply.
func NewDatabaseHandler(svc *database.Service, apps *application.Service, forward *portforward.Service, secrets *secret.Service, users *repositories.UserRepository, auditLog *audit.Logger, cluster ClusterCap) *DatabaseHandler {
	return &DatabaseHandler{svc: svc, apps: apps, forward: forward, secrets: secrets, users: users, audit: auditLog, cluster: cluster}
}

// Such an attach deploys fine and only fails at runtime.
func (h *DatabaseHandler) reach(app *models.Application, inst *models.DatabaseInstance) error {
	return h.placer.reach(
		placement.Site{Kind: "application", Name: app.Name, ClusterID: app.ClusterID, ServerID: app.ServerID},
		placement.Site{Kind: "database", Name: inst.Name, ClusterID: inst.ClusterID, ServerID: inst.ServerID},
		func(id uint) bool { return h.cluster != nil && h.cluster.IsSwarm(id) },
	)
}

type CreateDatabaseRequest struct {
	Body struct {
		Name    string `json:"name" required:"true"`
		Engine  string `json:"engine" required:"true" enum:"postgres,mysql,mariadb,redis,mongodb,libsql"`
		Version string `json:"version"`
		// Location is where the database runs; empty uses the workspace's default location.
		Location string `json:"location"`
		// ServerID pins a node; platform admins only.
		ServerID uint `json:"server_id"`
		SizeMB   int  `json:"size_mb"` // data-volume capacity in MB (0 = unspecified)
		// Empty uses the workspace's default class; fixed at creation.
		StorageClass string `json:"storage_class"`
		// 0 = unlimited, or the engine's default size when the plan caps the database budget.
		MemoryMB int     `json:"memory_mb" min:"0"`
		CPUCores float64 `json:"cpu_cores" min:"0"`
		// Size names a database size (Enterprise) instead of memory and CPU; see GET /database-sizes.
		Size string `json:"size"`
	} `json:"body"`
}

// The instance restarts to apply them.
type ResizeDatabaseRequest struct {
	Body struct {
		MemoryMB int     `json:"memory_mb" min:"0"`
		CPUCores float64 `json:"cpu_cores" min:"0"`
		Size     string  `json:"size"`
	} `json:"body"`
}

type SetDatabasePlacementRequest struct {
	Body struct {
		Dedicated bool `json:"dedicated"`
	} `json:"body"`
}

func databaseResources(memoryMB int, cpuCores float64, size string) database.Resources {
	return database.Resources{
		MemoryBytes: int64(memoryMB) * 1024 * 1024, NanoCPUs: int64(math.Round(cpuCores * 1e9)), Size: strings.TrimSpace(size),
	}
}

// Sizes lists the database sizes the workspace may pick from (Enterprise), its plan's default first.
func (h *DatabaseHandler) Sizes(c *okapi.Context) error {
	offer, err := h.svc.SizesFor(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list database sizes", err)
	}
	return ok(c, offer)
}

func (h *DatabaseHandler) Create(c *okapi.Context, req *CreateDatabaseRequest) error {
	wsID := middlewares.WorkspaceID(c)
	var sizeBytes int64
	if req.Body.SizeMB > 0 {
		sizeBytes = int64(req.Body.SizeMB) * 1024 * 1024
	}
	placed, err := h.placer.place(c, placement.Request{Location: req.Body.Location, ServerID: req.Body.ServerID})
	if err != nil {
		if a := placementAbort(c, err); a != nil {
			return a
		}
		return c.AbortInternalServerError("failed to place the database", err)
	}
	inst, err := h.svc.Provision(c.Request().Context(), wsID, placed.ServerID, req.Body.Name, models.DBEngine(req.Body.Engine), req.Body.Version, sizeBytes, req.Body.StorageClass,
		databaseResources(req.Body.MemoryMB, req.Body.CPUCores, req.Body.Size), selfOwnerMeta(h.users, c), nil)
	if err != nil {
		if a := quotaAbort(c, err); a != nil {
			return a
		}
		if errors.Is(err, database.ErrUnsupportedEngine) {
			return c.AbortBadRequest("unsupported engine")
		}
		if errors.Is(err, database.ErrInvalidResources) {
			return c.AbortBadRequest(err.Error())
		}
		if errors.Is(err, database.ErrSizesUnavailable) {
			return c.AbortWithError(402, err)
		}
		if errors.Is(err, nodes.ErrNodeOffline) || errors.Is(err, node.ErrNodeCordoned) || errors.Is(err, node.ErrNodeNotFound) {
			return c.AbortWithError(409, err)
		}
		if errors.Is(err, storageclass.ErrNotFound) || errors.Is(err, storageclass.ErrDisabled) || errors.Is(err, storageclass.ErrNotOnNode) {
			return c.AbortBadRequest(err.Error())
		}
		if errors.Is(err, storageclass.ErrPathMissing) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to provision database", err)
	}
	h.record(c, wsID, "database.create", inst.ID)
	return created(c, inst)
}

func (h *DatabaseHandler) Engines(c *okapi.Context) error {
	return ok(c, h.svc.EngineDefaults())
}

func (h *DatabaseHandler) List(c *okapi.Context) error {
	dbs, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list databases", err)
	}
	return ok(c, dbs)
}

func (h *DatabaseHandler) Get(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	// Lazily refresh stale sizes in the background (non-blocking).
	h.svc.MaybeRefreshSizes(inst)
	return ok(c, inst)
}

func (h *DatabaseHandler) Status(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	return ok(c, h.svc.LiveStatus(c.Request().Context(), inst))
}

func (h *DatabaseHandler) Events(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	return h.svc.StreamStatus(c.Request().Context(), inst, func(e eventbus.Event) error {
		return c.SSESendJSON(e)
	})
}

func (h *DatabaseHandler) WorkspaceEvents(c *okapi.Context) error {
	return h.svc.StreamWorkspaceStatus(c.Request().Context(), middlewares.WorkspaceID(c), func(e eventbus.Event) error {
		return c.SSESendJSON(e)
	})
}

// Credentials returns the instance's admin/direct connection (admin only).
func (h *DatabaseHandler) Credentials(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	info, err := h.svc.InstanceConnection(inst)
	if err != nil {
		return c.AbortInternalServerError("failed to read credentials", err)
	}
	h.record(c, inst.WorkspaceID, "database.reveal_credentials", inst.ID)
	return ok(c, info)
}

// Source-IP-gated so an external client can connect without publishing a host port.
func (h *DatabaseHandler) OpenForward(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if inst.Status != models.DBStatusRunning {
		return c.AbortWithError(409, errors.New("start the database before opening a forward"))
	}
	sess, err := h.forward.Open(c.Request().Context(), portforward.Target{
		InstanceID:  inst.ID,
		WorkspaceID: inst.WorkspaceID,
		ServerID:    inst.ServerID,
		Host:        inst.Host,
		Port:        inst.Port,
		Network:     inst.NetworkName,
	}, c.RealIP())
	if err != nil {
		if errors.Is(err, portforward.ErrNodeOffline) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to open forward", err)
	}
	h.record(c, inst.WorkspaceID, "database.forward_open", inst.ID)
	return created(c, sess)
}

// ListForwards lists the live forward sessions for an instance (admin only).
func (h *DatabaseHandler) ListForwards(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	all := h.forward.List(inst.WorkspaceID)
	out := make([]*portforward.Session, 0, len(all))
	for _, s := range all {
		if s.InstanceID == inst.ID {
			out = append(out, s)
		}
	}
	return ok(c, out)
}

// CloseForward tears down a forward session (admin only).
func (h *DatabaseHandler) CloseForward(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if err := h.forward.Close(inst.WorkspaceID, c.Param("sessionID")); err != nil {
		return c.AbortNotFound("forward session not found")
	}
	h.record(c, inst.WorkspaceID, "database.forward_close", inst.ID)
	return message(c, "forward closed")
}

func (h *DatabaseHandler) SyncSizes(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if err := h.svc.SyncSizes(c.Request().Context(), inst); err != nil {
		if errors.Is(err, database.ErrInstanceNotReady) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to sync sizes", err)
	}
	h.record(c, inst.WorkspaceID, "database.sync_sizes", inst.ID)
	return ok(c, inst)
}

// Start starts a stopped database instance container.
func (h *DatabaseHandler) Start(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if err := h.svc.Start(c.Request().Context(), inst); err != nil {
		return h.mapInstanceErr(c, err)
	}
	h.record(c, inst.WorkspaceID, "database.start", inst.ID)
	return message(c, "database started")
}

// Stop stops a running database instance container (data is retained).
func (h *DatabaseHandler) Stop(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if err := h.svc.Stop(c.Request().Context(), inst); err != nil {
		return h.mapInstanceErr(c, err)
	}
	h.record(c, inst.WorkspaceID, "database.stop", inst.ID)
	return message(c, "database stopped")
}

// Restart restarts a database instance container.
func (h *DatabaseHandler) Restart(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if err := h.svc.Restart(c.Request().Context(), inst); err != nil {
		return h.mapInstanceErr(c, err)
	}
	h.record(c, inst.WorkspaceID, "database.restart", inst.ID)
	return message(c, "database restarted")
}

// Redeploy recreates a database instance's container on its existing data volume.
func (h *DatabaseHandler) Redeploy(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if err := h.svc.Redeploy(c.Request().Context(), inst); err != nil {
		return h.mapInstanceErr(c, err)
	}
	h.record(c, inst.WorkspaceID, "database.redeploy", inst.ID)
	return message(c, "database redeploy queued")
}

// SetPlacement marks the instance dedicated (never reused by auto/shared placement) or open for sharing.
func (h *DatabaseHandler) SetPlacement(c *okapi.Context, req *SetDatabasePlacementRequest) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	updated, err := h.svc.SetDedicated(inst.WorkspaceID, inst.ID, req.Body.Dedicated)
	if err != nil {
		return h.mapDBErr(c, err)
	}
	action := "database.placement_shared"
	if req.Body.Dedicated {
		action = "database.placement_dedicated"
	}
	h.record(c, inst.WorkspaceID, action, inst.ID)
	return ok(c, updated)
}

// Resize sets the instance's CPU and memory limits; the container is recreated in the background to apply them.
func (h *DatabaseHandler) Resize(c *okapi.Context, req *ResizeDatabaseRequest) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	updated, err := h.svc.Resize(c.Request().Context(), inst, databaseResources(req.Body.MemoryMB, req.Body.CPUCores, req.Body.Size))
	if err != nil {
		if a := quotaAbort(c, err); a != nil {
			return a
		}
		switch {
		case errors.Is(err, database.ErrInvalidResources):
			return c.AbortBadRequest(err.Error())
		case errors.Is(err, database.ErrSizesUnavailable):
			return c.AbortWithError(402, err)
		case errors.Is(err, database.ErrInstanceBusy):
			return c.AbortWithError(409, err)
		}
		return h.mapInstanceErr(c, err)
	}
	h.record(c, inst.WorkspaceID, "database.resize", inst.ID)
	return ok(c, updated)
}

func (h *DatabaseHandler) UpgradeOptions(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if v := c.Query("version"); v != "" {
		plan, perr := h.svc.PlanUpgrade(inst.WorkspaceID, inst.ID, v)
		if perr != nil {
			return h.mapUpgradeErr(c, perr)
		}
		return ok(c, plan)
	}
	opts, err := h.svc.UpgradeOptions(inst.WorkspaceID, inst.ID)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	return ok(c, opts)
}

// UpgradeDatabaseRequest is the body for starting a version upgrade.
type UpgradeDatabaseRequest struct {
	Body struct {
		Version  string `json:"version" required:"true"` // target engine version (e.g. "17")
		StopApps bool   `json:"stop_apps"`               // auto-stop apps using the database during a copy upgrade
	} `json:"body"`
}

func (h *DatabaseHandler) Upgrade(c *okapi.Context, req *UpgradeDatabaseRequest) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	updated, err := h.svc.Upgrade(c.Request().Context(), inst.WorkspaceID, inst.ID, req.Body.Version, req.Body.StopApps)
	if err != nil {
		return h.mapUpgradeErr(c, err)
	}
	h.record(c, inst.WorkspaceID, "database.upgrade", inst.ID)
	return ok(c, updated)
}

func (h *DatabaseHandler) mapUpgradeErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, database.ErrNotFound):
		return c.AbortNotFound("database not found")
	case errors.Is(err, database.ErrInvalidVersion), errors.Is(err, database.ErrAlreadyOnVersion), errors.Is(err, database.ErrDowngrade):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, database.ErrUpgradeInProgress), errors.Is(err, database.ErrInstanceNotUpgradable):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("upgrade failed", err)
	}
}

// Logs streams the instance container's logs over SSE.
func (h *DatabaseHandler) Logs(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	// follow defaults to true (live tail); ?follow=false yields a one-shot snapshot.
	follow := c.Query("follow") != "false"
	err = h.svc.StreamLogs(c.Request().Context(), inst.WorkspaceID, inst.ID, follow, c.Query("tail"), func(l docker.LogLine) error {
		return c.SSESendJSON(l)
	})
	if errors.Is(err, database.ErrNoContainer) {
		return c.AbortWithError(409, err)
	}
	return err
}

func (h *DatabaseHandler) Delete(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if err := h.svc.Delete(c.Request().Context(), inst); err != nil {
		if errors.Is(err, database.ErrInstanceInUse) || errors.Is(err, database.ErrInstanceLinked) || errors.Is(err, database.ErrInstanceRunning) || errors.Is(err, database.ErrInstanceOwned) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to delete database", err)
	}
	h.record(c, inst.WorkspaceID, "database.delete", inst.ID)
	return message(c, "database deleted")
}

type CreateLogicalDatabaseRequest struct {
	Body struct {
		Name          string `json:"name" required:"true"`
		ApplicationID *uint  `json:"application_id"`
	} `json:"body"`
}

func (h *DatabaseHandler) ListDatabases(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	dbs, err := h.svc.ListDatabases(inst.WorkspaceID, inst.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list databases", err)
	}
	return ok(c, dbs)
}

func (h *DatabaseHandler) CreateDatabase(c *okapi.Context, req *CreateLogicalDatabaseRequest) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	// Only manual creation is refused: an explicit instance pin in a manifest goes through the service.
	if database.IsDedicated(inst) {
		return h.mapDBErr(c, database.ErrInstanceDedicated)
	}
	wsID := inst.WorkspaceID
	var app *models.Application
	var env database.EnvLink
	if req.Body.ApplicationID != nil {
		if app, err = h.apps.Get(wsID, *req.Body.ApplicationID); err != nil {
			return c.AbortNotFound("application not found")
		}
		if err := h.reach(app, inst); err != nil {
			return c.AbortBadRequest(err.Error())
		}
		if env, err = h.svc.ResolveEnvLink(wsID, app.ID, inst.Engine, "", nil, 0, 0); err != nil {
			return h.mapDBErr(c, err)
		}
	}
	db, err := h.svc.CreateDatabase(c.Request().Context(), wsID, inst.ID, req.Body.Name, req.Body.ApplicationID)
	if err != nil {
		return h.mapDBErr(c, err)
	}
	h.record(c, wsID, "database.db_create", inst.ID)

	injected := false
	if app != nil {
		if _, err := h.svc.AttachToApp(wsID, db.ID, app.ID, env); err == nil {
			injected = h.injectIntoApp(app, inst, db, env, nil)
		}
	}
	return created(c, map[string]any{"database": db, "env_injected": injected})
}

// DatabaseConnection reveals a logical database's connection (admin only).
func (h *DatabaseHandler) DatabaseConnection(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	db, err := h.svc.GetDatabase(inst.WorkspaceID, h.dbID(c))
	if err != nil || db.InstanceID != inst.ID {
		return c.AbortNotFound("logical database not found")
	}
	info, err := h.svc.DatabaseConnection(inst, db)
	if err != nil {
		return c.AbortInternalServerError("failed to read credentials", err)
	}
	h.record(c, inst.WorkspaceID, "database.db_reveal", db.ID)
	return ok(c, info)
}

func (h *DatabaseHandler) DeleteDatabase(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	// Check the instance in the URL too, or a user could delete a logical DB of another instance.
	db, err := h.svc.GetDatabase(inst.WorkspaceID, h.dbID(c))
	if err != nil || db.InstanceID != inst.ID {
		return c.AbortNotFound("logical database not found")
	}
	if err := h.svc.DeleteDatabase(c.Request().Context(), inst.WorkspaceID, db.ID); err != nil {
		return h.mapDBErr(c, err)
	}
	h.record(c, inst.WorkspaceID, "database.db_delete", db.ID)
	return message(c, "database deleted")
}

// Idempotent: on a database that has one it re-applies the grants.
func (h *DatabaseHandler) EnableReadOnlyUser(c *okapi.Context) error {
	return h.setReadOnly(c, true)
}

// DisableReadOnlyUser removes a logical database's read-only user.
func (h *DatabaseHandler) DisableReadOnlyUser(c *okapi.Context) error {
	return h.setReadOnly(c, false)
}

func (h *DatabaseHandler) setReadOnly(c *okapi.Context, on bool) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	db, err := h.svc.GetDatabase(inst.WorkspaceID, h.dbID(c))
	if err != nil || db.InstanceID != inst.ID {
		return c.AbortNotFound("logical database not found")
	}
	ctx := c.Request().Context()
	if on {
		err = h.svc.EnableReadOnly(ctx, db)
	} else {
		err = h.svc.DisableReadOnly(ctx, db)
	}
	if errors.Is(err, database.ErrReadOnlyUnsupported) {
		return c.AbortBadRequest(err.Error())
	}
	if err != nil {
		return c.AbortInternalServerError("failed to change the read-only user", err)
	}
	action := "database.db_readonly_disable"
	if on {
		action = "database.db_readonly_enable"
	}
	h.record(c, inst.WorkspaceID, action, db.ID)
	return ok(c, db)
}

// ListByApp lists the logical databases attached to an application.
func (h *DatabaseHandler) ListByApp(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := appParamInt(c)
	if err != nil || appID <= 0 {
		return c.AbortBadRequest("invalid app id")
	}
	list, err := h.svc.ListByApp(wsID, uint(appID))
	if err != nil {
		return c.AbortInternalServerError("failed to list databases", err)
	}
	return ok(c, list)
}

// Uses the app's own scoped credentials, not the instance admin ones.
func (h *DatabaseHandler) AppDatabaseConnection(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := appParamInt(c)
	if err != nil || appID <= 0 {
		return c.AbortBadRequest("invalid app id")
	}
	info, err := h.svc.DatabaseConnectionForApp(wsID, uint(appID), h.dbID(c))
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	h.record(c, wsID, "database.app_reveal", uint(appID))
	return ok(c, info)
}

type AttachDatabaseRequest struct {
	Body struct {
		// Empty means DB_*.
		EnvPrefix string `json:"env_prefix"`
		// An empty name skips the field.
		EnvMap map[string]string `json:"env_map"`
	} `json:"body"`
}

// The database must be on the app's node and not already owned by a different app.
func (h *DatabaseHandler) AttachToApp(c *okapi.Context, req *AttachDatabaseRequest) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := appParamInt(c)
	if err != nil || appID <= 0 {
		return c.AbortBadRequest("invalid app id")
	}
	app, err := h.apps.Get(wsID, uint(appID))
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	db, err := h.svc.GetDatabase(wsID, h.dbID(c))
	if err != nil {
		return c.AbortNotFound("logical database not found")
	}
	inst, err := h.svc.Get(wsID, db.InstanceID)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	if err := h.reach(app, inst); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if db.ApplicationID != nil && *db.ApplicationID != uint(appID) {
		return c.AbortWithError(409, errors.New("this database is already attached to another application"))
	}
	if err := h.svc.CheckDedicatedAttach(inst, uint(appID)); err != nil {
		return h.mapDBErr(c, err)
	}
	prefix := sanitizeEnvPrefix(req.Body.EnvPrefix)
	env, err := h.svc.ResolveEnvLink(wsID, uint(appID), inst.Engine, prefix, req.Body.EnvMap, db.ID, 0)
	if err != nil {
		return h.mapDBErr(c, err)
	}
	var previous []string
	if db.ApplicationID != nil {
		previous = database.InjectedKeys(inst.Engine, db.EnvPrefix, db.EnvMap, db.EnvVars)
	}
	if _, err := h.svc.AttachToApp(wsID, db.ID, uint(appID), env); err != nil {
		return h.mapDBErr(c, err)
	}
	injected := h.injectIntoApp(app, inst, db, env, previous)
	h.record(c, wsID, "database.attach_app", db.ID)
	return ok(c, map[string]any{"database": db, "env_injected": injected, "env_vars": env.Vars})
}

func (h *DatabaseHandler) DetachFromApp(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := appParamInt(c)
	if err != nil || appID <= 0 {
		return c.AbortBadRequest("invalid app id")
	}
	db, err := h.svc.GetDatabase(wsID, h.dbID(c))
	if err != nil {
		return c.AbortNotFound("logical database not found")
	}
	if db.ApplicationID == nil || *db.ApplicationID != uint(appID) {
		return c.AbortNotFound("database is not attached to this application")
	}
	inst, err := h.svc.Get(wsID, db.InstanceID)
	if err != nil {
		return c.AbortNotFound("database instance not found")
	}
	keys := database.InjectedKeys(inst.Engine, db.EnvPrefix, db.EnvMap, db.EnvVars)
	if _, err := h.svc.DetachFromApp(wsID, db.ID); err != nil {
		return h.mapDBErr(c, err)
	}
	h.removeFromApp(wsID, uint(appID), keys)
	h.record(c, wsID, "database.detach_app", db.ID)
	return message(c, "database detached")
}

// Unlike a logical database, one instance can serve many apps.
func (h *DatabaseHandler) LinkInstanceToApp(c *okapi.Context, req *AttachDatabaseRequest) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := appParamInt(c)
	if err != nil || appID <= 0 {
		return c.AbortBadRequest("invalid app id")
	}
	app, err := h.apps.Get(wsID, uint(appID))
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	if models.EngineUsesLogicalDatabaseRecord(inst.Engine) {
		return c.AbortBadRequest(database.ErrLinkLogicalDatabase.Error())
	}
	if err := h.reach(app, inst); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	prefix := sanitizeEnvPrefix(req.Body.EnvPrefix)
	env, err := h.svc.ResolveEnvLink(wsID, uint(appID), inst.Engine, prefix, req.Body.EnvMap, 0, inst.ID)
	if err != nil {
		return h.mapDBErr(c, err)
	}
	var previous []string
	if l, err := h.svc.InstanceLinkForApp(wsID, inst.ID, uint(appID)); err == nil {
		previous = database.InjectedKeys(inst.Engine, l.EnvPrefix, l.EnvMap, l.EnvVars)
	}
	link, err := h.svc.LinkInstance(wsID, inst.ID, uint(appID), env)
	if err != nil {
		return h.mapDBErr(c, err)
	}
	injected := h.injectIntoApp(app, inst, nil, env, previous)
	h.record(c, wsID, "database.link_app", inst.ID)
	return ok(c, map[string]any{"link": link, "env_injected": injected, "env_vars": env.Vars})
}

func (h *DatabaseHandler) UnlinkInstanceFromApp(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := appParamInt(c)
	if err != nil || appID <= 0 {
		return c.AbortBadRequest("invalid app id")
	}
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	l, err := h.svc.UnlinkInstance(wsID, inst.ID, uint(appID))
	if err != nil {
		return c.AbortNotFound("database is not linked to this application")
	}
	h.removeFromApp(wsID, uint(appID), database.InjectedKeys(inst.Engine, l.EnvPrefix, l.EnvMap, l.EnvVars))
	h.record(c, wsID, "database.unlink_app", inst.ID)
	return message(c, "database unlinked")
}

// AppInstanceConnection reveals the connection of an instance linked to the app.
func (h *DatabaseHandler) AppInstanceConnection(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := appParamInt(c)
	if err != nil || appID <= 0 {
		return c.AbortBadRequest("invalid app id")
	}
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	info, err := h.svc.InstanceConnectionForApp(wsID, uint(appID), inst.ID)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	h.record(c, wsID, "database.app_reveal", uint(appID))
	return ok(c, info)
}

// d is nil for an instance link.
func (h *DatabaseHandler) injectIntoApp(app *models.Application, inst *models.DatabaseInstance, d *models.Database, env database.EnvLink, previous []string) bool {
	vars, err := h.svc.AppEnv(inst, d, env.Prefix, env.Map)
	if err != nil {
		return false
	}
	for _, k := range previous {
		if !slices.Contains(env.Vars, k) {
			_ = h.apps.DeleteEnvVar(app.ID, k)
		}
	}
	for _, e := range vars {
		_ = h.apps.SetEnvVar(app.ID, e.Key, e.Value, e.Secret)
	}
	_, _ = h.apps.MarkRedeployRequired(app)
	return true
}

func sanitizeEnvPrefix(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == ' ':
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func (h *DatabaseHandler) removeFromApp(workspaceID, appID uint, keys []string) {
	app, err := h.apps.Get(workspaceID, appID)
	if err != nil {
		return
	}
	for _, k := range keys {
		_ = h.apps.DeleteEnvVar(app.ID, k)
	}
	_, _ = h.apps.MarkRedeployRequired(app)
}

// AttachNetwork connects the instance to a workspace network (live — no restart).
func (h *DatabaseHandler) AttachNetwork(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	netID, perr := strconv.Atoi(c.Param("networkID"))
	if perr != nil || netID <= 0 {
		return c.AbortBadRequest("invalid network id")
	}
	updated, err := h.svc.AttachNetwork(c.Request().Context(), inst.WorkspaceID, inst.ID, uint(netID))
	if err != nil {
		return h.mapNetworkErr(c, err)
	}
	h.record(c, inst.WorkspaceID, "database.network_attach", inst.ID)
	return ok(c, updated)
}

// DetachNetwork disconnects the instance from a network (the default cannot be removed).
func (h *DatabaseHandler) DetachNetwork(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	netID, perr := strconv.Atoi(c.Param("networkID"))
	if perr != nil || netID <= 0 {
		return c.AbortBadRequest("invalid network id")
	}
	updated, err := h.svc.DetachNetwork(c.Request().Context(), inst.WorkspaceID, inst.ID, uint(netID))
	if err != nil {
		return h.mapNetworkErr(c, err)
	}
	h.record(c, inst.WorkspaceID, "database.network_detach", inst.ID)
	return ok(c, updated)
}

func (h *DatabaseHandler) mapNetworkErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, database.ErrDefaultNetwork):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, database.ErrNoNetworkProvider):
		return c.AbortWithError(409, err)
	case errors.Is(err, database.ErrNotFound):
		return c.AbortNotFound("not found")
	case errors.Is(err, database.ErrInvalidEnvMap), errors.Is(err, database.ErrLinkLogicalDatabase):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, database.ErrEnvConflict), errors.Is(err, database.ErrInstanceLinked), errors.Is(err, database.ErrInstanceDedicated):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("network operation failed", err)
	}
}

func (h *DatabaseHandler) load(c *okapi.Context) (*models.DatabaseInstance, error) {
	id, err := resolveID(c.Param("databaseID"), h.svc.IDByUID)
	if err != nil {
		return nil, errors.New("invalid database id")
	}
	return h.svc.Get(middlewares.WorkspaceID(c), id)
}

func (h *DatabaseHandler) dbID(c *okapi.Context) uint {
	id, _ := strconv.Atoi(c.Param("dbID"))
	return uint(id)
}

func (h *DatabaseHandler) mapInstanceErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, database.ErrNoContainer), errors.Is(err, datavolume.ErrLost), errors.Is(err, database.ErrNodeGone),
		errors.Is(err, database.ErrUpgradeInProgress), errors.Is(err, database.ErrInstanceBusy):
		return c.AbortWithError(409, err)
	case errors.Is(err, database.ErrUnsupportedEngine):
		return c.AbortBadRequest(err.Error())
	}
	return c.AbortInternalServerError("database operation failed", err)
}

func (h *DatabaseHandler) mapDBErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, database.ErrNoLogicalDBs):
		return c.AbortBadRequest("this engine does not support multiple databases")
	case errors.Is(err, database.ErrInstanceNotReady):
		return c.AbortWithError(409, err)
	case errors.Is(err, database.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, database.ErrNotFound):
		return c.AbortNotFound("not found")
	case errors.Is(err, database.ErrInvalidEnvMap), errors.Is(err, database.ErrLinkLogicalDatabase):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, database.ErrEnvConflict), errors.Is(err, database.ErrInstanceLinked):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("database operation failed", err)
	}
}

func (h *DatabaseHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "database", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
