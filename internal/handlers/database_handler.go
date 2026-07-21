// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/node"
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
}

// NewDatabaseHandler builds the handler. cluster may be nil (never cluster mode),
// in which case the co-location guards below always apply.
func NewDatabaseHandler(svc *database.Service, apps *application.Service, forward *portforward.Service, secrets *secret.Service, users *repositories.UserRepository, auditLog *audit.Logger, cluster ClusterCap) *DatabaseHandler {
	return &DatabaseHandler{svc: svc, apps: apps, forward: forward, secrets: secrets, users: users, audit: auditLog, cluster: cluster}
}

// crossNodeOK reports whether an app may attach to a database on another node.
// Only in cluster mode: the workspace network is then a swarm overlay, so the
// instance's DNS alias resolves and connects from any node. Without it, every
// workspace network is a node-local bridge and a cross-node attach would produce
// an app that deploys fine and then fails at runtime with "could not translate
// host name" — so it is rejected up front instead.
func (h *DatabaseHandler) crossNodeOK() bool {
	return h.cluster != nil && h.cluster.CapCluster()
}

// --- Instances ---

type CreateDatabaseRequest struct {
	Body struct {
		Name     string `json:"name" required:"true"`
		Engine   string `json:"engine" required:"true" enum:"postgres,mysql,mariadb,redis,mongodb,libsql"`
		Version  string `json:"version"`
		ServerID uint   `json:"server_id"` // node to place on (0 = local)
		SizeMB   int    `json:"size_mb"`   // data-volume capacity in MB (0 = unspecified)
	} `json:"body"`
}

// Create provisions a new database server instance (the container is brought up
// asynchronously by the worker).
func (h *DatabaseHandler) Create(c *okapi.Context, req *CreateDatabaseRequest) error {
	wsID := middlewares.WorkspaceID(c)
	var sizeBytes int64
	if req.Body.SizeMB > 0 {
		sizeBytes = int64(req.Body.SizeMB) * 1024 * 1024
	}
	inst, err := h.svc.Provision(c.Request().Context(), wsID, req.Body.ServerID, req.Body.Name, models.DBEngine(req.Body.Engine), req.Body.Version, sizeBytes, selfOwnerMeta(h.users, c), nil)
	if err != nil {
		if a := quotaAbort(c, err); a != nil {
			return a
		}
		if errors.Is(err, database.ErrUnsupportedEngine) {
			return c.AbortBadRequest("unsupported engine")
		}
		if errors.Is(err, nodes.ErrNodeOffline) || errors.Is(err, node.ErrNodeCordoned) || errors.Is(err, node.ErrNodeNotFound) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to provision database", err)
	}
	h.record(c, wsID, "database.create", inst.ID)
	return created(c, inst)
}

// Engines returns the resolved default image/version per engine (from the
// deployment-config catalog), so the create form prefills the configured tags.
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

// Status returns the instance's live status (stored lifecycle status plus the
// container's real-time state), for the detail page to poll while provisioning,
// upgrading, or restarting — so the user sees the outcome without a refresh.
func (h *DatabaseHandler) Status(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	return ok(c, h.svc.LiveStatus(c.Request().Context(), inst))
}

// Events streams the instance's live status — provisioning progress, upgrade
// phases, and start/stop transitions — over SSE, so the detail page reflects
// changes the moment they happen instead of polling.
func (h *DatabaseHandler) Events(c *okapi.Context) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	return h.svc.StreamStatus(c.Request().Context(), inst, func(e eventbus.Event) error {
		return c.SSESendJSON(e)
	})
}

// WorkspaceEvents streams lifecycle status changes for every database instance in
// the workspace over SSE, so the Databases list updates rows live (one connection)
// instead of polling.
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

// --- Port-forward (on-demand external access) ---

// OpenForward starts an ephemeral, source-IP-gated TCP forward to the instance
// so an external DB client can connect without publishing a host port. Returns
// the connection endpoint and its expiry (admin only).
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

// SyncSizes refreshes the instance's and its databases' on-disk sizes by
// querying the engine (instance must be running).
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

// UpgradeOptions returns the suggested upgrade targets + affected apps for an
// instance, or — when ?version= is supplied — the resolved plan for that target
// (path, major, apps), surfacing downgrade/same-version errors as 400s.
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

// Upgrade starts a background version upgrade and returns the instance marked
// "upgrading"; clients poll Get for progress.
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

// mapUpgradeErr maps upgrade validation failures to 400/409 rather than 500.
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
		if errors.Is(err, database.ErrInstanceInUse) || errors.Is(err, database.ErrInstanceRunning) || errors.Is(err, database.ErrInstanceOwned) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to delete database", err)
	}
	h.record(c, inst.WorkspaceID, "database.delete", inst.ID)
	return message(c, "database deleted")
}

// --- Logical databases ---

type CreateLogicalDatabaseRequest struct {
	Body struct {
		Name string `json:"name" required:"true"`
		// ApplicationID optionally attaches the database to an app: its
		// connection is injected as env vars and the app redeploys.
		ApplicationID *uint `json:"application_id"`
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

// CreateDatabase provisions a logical database on the instance and, when an app
// is given, injects its connection into the app's env and redeploys it.
func (h *DatabaseHandler) CreateDatabase(c *okapi.Context, req *CreateLogicalDatabaseRequest) error {
	inst, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("database not found")
	}
	wsID := inst.WorkspaceID
	// Co-location: outside cluster mode a database may only attach to an app on the
	// same node, because the workspace network is a node-local bridge.
	if req.Body.ApplicationID != nil && !h.crossNodeOK() {
		if app, aerr := h.apps.Get(wsID, *req.Body.ApplicationID); aerr == nil && app.ServerID != inst.ServerID {
			return c.AbortBadRequest("the application is on a different node than this database. Enable cluster networking to run apps and databases across nodes")
		}
	}
	db, err := h.svc.CreateDatabase(c.Request().Context(), wsID, inst.ID, req.Body.Name, req.Body.ApplicationID)
	if err != nil {
		return h.mapDBErr(c, err)
	}
	h.record(c, wsID, "database.db_create", inst.ID)

	injected := false
	if req.Body.ApplicationID != nil {
		if conn, err := h.svc.DatabaseConnection(inst, db); err == nil {
			injected = h.injectIntoApp(wsID, *req.Body.ApplicationID, inst, db, conn, "")
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
	// Verify the logical database actually belongs to the instance in the URL,
	// not just to the same workspace — otherwise the {id} instance segment is
	// unverified and a user could delete a logical DB of a different instance.
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

// --- App-scoped (databases attached to an application) ---

// ListByApp lists the logical databases attached to an application.
func (h *DatabaseHandler) ListByApp(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := strconv.Atoi(c.Param("appID"))
	if err != nil || appID <= 0 {
		return c.AbortBadRequest("invalid app id")
	}
	list, err := h.svc.ListByApp(wsID, uint(appID))
	if err != nil {
		return c.AbortInternalServerError("failed to list databases", err)
	}
	return ok(c, list)
}

// AppDatabaseConnection reveals the connection for one of the app's databases —
// the app's own scoped credentials, not the instance admin credentials.
func (h *DatabaseHandler) AppDatabaseConnection(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := strconv.Atoi(c.Param("appID"))
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

// AttachDatabaseRequest links an existing logical database to an app.
type AttachDatabaseRequest struct {
	Body struct {
		// EnvPrefix optionally namespaces the injected connection vars
		// (e.g. "ANALYTICS" -> ANALYTICS_DATABASE_URL); empty = DATABASE_URL/DB_*.
		EnvPrefix string `json:"env_prefix"`
	} `json:"body"`
}

// AttachToApp links an existing logical database (by dbID) to the app and
// injects its connection. The database must be on the app's node and not
// already owned by a different app.
func (h *DatabaseHandler) AttachToApp(c *okapi.Context, req *AttachDatabaseRequest) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := strconv.Atoi(c.Param("appID"))
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
	if app.ServerID != inst.ServerID && !h.crossNodeOK() {
		return c.AbortBadRequest("the application is on a different node than this database. Enable cluster networking to run apps and databases across nodes")
	}
	if db.ApplicationID != nil && *db.ApplicationID != uint(appID) {
		return c.AbortWithError(409, errors.New("this database is already attached to another application"))
	}
	prefix := sanitizeEnvPrefix(req.Body.EnvPrefix)
	if _, err := h.svc.AttachToApp(wsID, db.ID, uint(appID), prefix); err != nil {
		return h.mapDBErr(c, err)
	}
	injected := false
	if conn, err := h.svc.DatabaseConnection(inst, db); err == nil {
		injected = h.injectIntoApp(wsID, uint(appID), inst, db, conn, prefix)
	}
	h.record(c, wsID, "database.attach_app", db.ID)
	return ok(c, map[string]any{"database": db, "env_injected": injected})
}

// DetachFromApp unlinks a logical database from the app and removes the env
// vars its attachment injected.
func (h *DatabaseHandler) DetachFromApp(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	appID, err := strconv.Atoi(c.Param("appID"))
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
	prefix := db.EnvPrefix // capture before the service clears it
	if _, err := h.svc.DetachFromApp(wsID, db.ID); err != nil {
		return h.mapDBErr(c, err)
	}
	h.removeFromApp(wsID, uint(appID), prefix)
	h.record(c, wsID, "database.detach_app", db.ID)
	return message(c, "database detached")
}

// injectIntoApp writes the connection as env vars on the app and flags it for
// redeploy. The password and URL are injected as references to the database's
// auto-provisioned Vault secrets (`${{ secrets.NAME }}`), so the app env never
// holds the plaintext and rotating the database propagates to every consumer.
// Falls back to plaintext when the Vault is unavailable.
func (h *DatabaseHandler) injectIntoApp(workspaceID, appID uint, inst *models.DatabaseInstance, db *models.Database, conn database.ConnectionInfo, prefix string) bool {
	app, err := h.apps.Get(workspaceID, appID)
	if err != nil {
		return false
	}

	passVal := conn.Password
	uriVal := conn.URI
	passSecret := true
	uriSecret := true
	if h.secrets != nil {
		passVal = "${{ secrets." + database.PasswordSecretName(inst, db) + " }}"
		passSecret = false // the reference itself is not sensitive
		if conn.URI != "" {
			uriVal = "${{ secrets." + database.URLSecretName(inst, db) + " }}"
			uriSecret = false
		}
	}

	vars := []struct {
		k, v   string
		secret bool
	}{
		{"DATABASE_URL", uriVal, uriSecret},
		{"DB_HOST", conn.Host, false},
		{"DB_PORT", strconv.Itoa(conn.Port), false},
		{"DB_NAME", conn.Database, false},
		{"DB_USER", conn.Username, false},
		{"DB_PASSWORD", passVal, passSecret},
	}
	for _, e := range vars {
		if e.v == "" {
			continue
		}
		_ = h.apps.SetEnvVar(app.ID, envKey(prefix, e.k), e.v, e.secret)
	}
	_, _ = h.apps.MarkRedeployRequired(app)
	return true
}

// dbEnvBaseKeys are the connection env vars injected per attached database
// (before any prefix).
var dbEnvBaseKeys = []string{"DATABASE_URL", "DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD"}

// envKey applies an optional prefix to a base key ("ANALYTICS" + "DB_HOST" ->
// "ANALYTICS_DB_HOST"); an empty prefix leaves the base key unchanged.
func envKey(prefix, base string) string {
	if prefix == "" {
		return base
	}
	return prefix + "_" + base
}

// sanitizeEnvPrefix normalizes a user-supplied prefix to an upper-snake token
// ([A-Z0-9_], no leading/trailing underscore); invalid input collapses to "".
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

// removeFromApp deletes the connection env vars previously injected for a
// database (using its recorded prefix) and flags the app for redeploy.
func (h *DatabaseHandler) removeFromApp(workspaceID, appID uint, prefix string) {
	app, err := h.apps.Get(workspaceID, appID)
	if err != nil {
		return
	}
	for _, base := range dbEnvBaseKeys {
		_ = h.apps.DeleteEnvVar(app.ID, envKey(prefix, base))
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

// mapInstanceErr maps instance lifecycle (start/stop/restart) errors.
func (h *DatabaseHandler) mapInstanceErr(c *okapi.Context, err error) error {
	if errors.Is(err, database.ErrNoContainer) {
		return c.AbortWithError(409, err)
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
	default:
		return c.AbortInternalServerError("database operation failed", err)
	}
}

func (h *DatabaseHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "database", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
