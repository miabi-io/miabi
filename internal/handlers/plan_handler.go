// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// PlanHandler manages the plan catalog and workspace assignment (platform admin).
type PlanHandler struct {
	repo       *repositories.PlanRepository
	overrides  *repositories.WorkspaceQuotaRepository
	workspaces *repositories.WorkspaceRepository
	ee         enterprise.EE
	audit      *audit.Logger
}

func NewPlanHandler(repo *repositories.PlanRepository, overrides *repositories.WorkspaceQuotaRepository, workspaces *repositories.WorkspaceRepository, ee enterprise.EE, auditLog *audit.Logger) *PlanHandler {
	return &PlanHandler{repo: repo, overrides: overrides, workspaces: workspaces, ee: ee, audit: auditLog}
}

// PlanBody carries every settable plan field. Limits use -1 = unlimited, 0 = none.
type PlanBody struct {
	Name                      string `json:"name"`
	Description               string `json:"description"`
	IsDefault                 bool   `json:"is_default"`
	IsActive                  bool   `json:"is_active"`
	MaxApps                   int    `json:"max_apps"`
	MaxDatabaseInstances      int    `json:"max_database_instances"`
	MaxCronJobs               int    `json:"max_cron_jobs"`
	MaxVolumes                int    `json:"max_volumes"`
	MaxNetworks               int    `json:"max_networks"`
	MaxAPIKeys                int    `json:"max_api_keys"`
	MaxMembers                int    `json:"max_members"`
	MaxDatabasesPerInstance   int    `json:"max_databases_per_instance"`
	MaxCPUCores               int    `json:"max_cpu_cores"`
	MaxMemoryMB               int    `json:"max_memory_mb"`
	MaxDatabaseInstanceSizeMB int    `json:"max_database_instance_size_mb"`
	MaxStorageMB              int    `json:"max_storage_mb"`
	MaxRunners                int    `json:"max_runners"`
	MaxGPUs                   int    `json:"max_gpus"`
	AllowCustomTLS            bool   `json:"allow_custom_tls"`
	AllowPrivilegedHostMounts bool   `json:"allow_privileged_host_mounts"`
	AllowShellExec            bool   `json:"allow_shell_exec"`
	AllowSharedStorage        bool   `json:"allow_shared_storage"`
	AllowDNSProviders         bool   `json:"allow_dns_providers"`
	AllowCustomLabels         bool   `json:"allow_custom_labels"`
	AllowPlatformRunners      bool   `json:"allow_platform_runners"`
	AllowCustomBuilder        bool   `json:"allow_custom_builder"`
	AllowGPU                  bool   `json:"allow_gpu"`
	SecurityProfile           string `json:"security_profile"`          // "default" | "restricted"
	AllowOfficialImageUser    bool   `json:"allow_official_image_user"` // exempt official-template apps from the restricted UID
}

type CreatePlanRequest struct {
	Body struct {
		PlanBody
		Name string `json:"name" required:"true"`
	} `json:"body"`
}

type UpdatePlanRequest struct {
	Body struct {
		PlanBody
	} `json:"body"`
}

type AssignWorkspacePlanRequest struct {
	Body struct {
		PlanID *uint `json:"plan_id"` // null clears the assignment (falls back to default)
	} `json:"body"`
}

// Refusals protecting the platform's own plan. 409 rather than 403: the request
// is well-formed and the caller is authorized — this row is simply not theirs to
// change that way.
var (
	errSystemPlanDelete  = errors.New("the platform's system plan cannot be deleted; it is pinned to the system workspace")
	errSystemPlanRename  = errors.New("the platform's system plan cannot be renamed; it is resolved by name")
	errSystemPlanDefault = errors.New("the platform's system plan cannot be the default; unassigned workspaces would get unlimited resources")
)

// systemPlanEdit reports why an edit to the platform's own plan is refused, or
// nil when it is allowed.
//
// Its limits stay editable — an operator may reasonably want the system
// workspace bounded. Its identity does not: the name is how
// workspace.pinUnlimitedPlan finds it, and making it the default would put every
// unassigned workspace on unlimited resources.
func systemPlanEdit(p *models.Plan, newName string, makeDefault bool) error {
	if p == nil || !p.System {
		return nil
	}
	if newName != "" && newName != p.Name {
		return errSystemPlanRename
	}
	if makeDefault {
		return errSystemPlanDefault
	}
	return nil
}

// guardSecurityProfile enforces that the Enterprise-only restricted security profile may be
// selected only with the security_profile entitlement; the default profile is always free. Uses
// RequireMutable so a degraded license keeps an existing value read-only. nil when not restricted.
func (h *PlanHandler) guardSecurityProfile(securityProfile string) error {
	if models.NormalizeSecurityProfile(securityProfile) == models.SecurityProfileRestricted {
		return h.ee.RequireMutable(enterprise.FlagSecurityProfile)
	}
	return nil
}

func (b PlanBody) apply(p *models.Plan) {
	p.Name = b.Name
	p.Description = b.Description
	p.IsActive = b.IsActive
	p.MaxApps = b.MaxApps
	p.MaxDatabaseInstances = b.MaxDatabaseInstances
	p.MaxCronJobs = b.MaxCronJobs
	p.MaxVolumes = b.MaxVolumes
	p.MaxNetworks = b.MaxNetworks
	p.MaxAPIKeys = b.MaxAPIKeys
	p.MaxMembers = b.MaxMembers
	p.MaxDatabasesPerInstance = b.MaxDatabasesPerInstance
	p.MaxCPUCores = b.MaxCPUCores
	p.MaxMemoryMB = b.MaxMemoryMB
	p.MaxDatabaseInstanceSizeMB = b.MaxDatabaseInstanceSizeMB
	p.MaxStorageMB = b.MaxStorageMB
	p.MaxRunners = b.MaxRunners
	p.MaxGPUs = b.MaxGPUs
	p.AllowCustomTLS = b.AllowCustomTLS
	p.AllowPrivilegedHostMounts = b.AllowPrivilegedHostMounts
	p.AllowShellExec = b.AllowShellExec
	p.AllowSharedStorage = b.AllowSharedStorage
	p.AllowDNSProviders = b.AllowDNSProviders
	p.AllowCustomLabels = b.AllowCustomLabels
	p.AllowPlatformRunners = b.AllowPlatformRunners
	p.AllowCustomBuilder = b.AllowCustomBuilder
	p.AllowGPU = b.AllowGPU
	p.SecurityProfile = models.NormalizeSecurityProfile(b.SecurityProfile)
	p.AllowOfficialImageUser = b.AllowOfficialImageUser
}

// List returns a paginated, searchable plan catalog.
func (h *PlanHandler) List(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	plans, total, err := h.repo.ListPaged(c.Query("search"), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list plans", err)
	}
	return paginated(c, plans, total, page, size)
}

func (h *PlanHandler) Get(c *okapi.Context) error {
	p, err := h.repo.FindByID(h.id(c))
	if err != nil {
		return c.AbortNotFound("plan not found")
	}
	return ok(c, p)
}

func (h *PlanHandler) Create(c *okapi.Context, req *CreatePlanRequest) error {
	// Edition plan-catalog cap. System plans (the platform's own Unlimited) do not
	// count, and an install that predates the cap being lowered keeps its old one —
	// see planLimit. A licence raises or lifts it.
	if lim := h.ee.Entitlements().PlanLimit(); lim >= 0 {
		if n, err := h.repo.Count(); err == nil && n >= int64(lim) {
			return entitlementAbort(c, enterprise.ErrPlanLimitReached)
		}
	}
	body := req.Body.PlanBody
	body.Name = req.Body.Name
	if err := h.guardSecurityProfile(body.SecurityProfile); err != nil {
		return entitlementAbort(c, err)
	}
	if body.IsDefault {
		_ = h.repo.ClearDefault(nil)
	}
	p := &models.Plan{IsDefault: body.IsDefault}
	body.apply(p)
	if err := h.repo.Create(p); err != nil {
		return c.AbortInternalServerError("failed to create plan", err)
	}
	h.record(c, "plan.create", p.ID)
	return created(c, p)
}

func (h *PlanHandler) Update(c *okapi.Context, req *UpdatePlanRequest) error {
	p, err := h.repo.FindByID(h.id(c))
	if err != nil {
		return c.AbortNotFound("plan not found")
	}
	if err := h.guardSecurityProfile(req.Body.SecurityProfile); err != nil {
		return entitlementAbort(c, err)
	}
	if err := systemPlanEdit(p, req.Body.Name, req.Body.IsDefault); err != nil {
		return c.AbortWithError(409, err)
	}
	if req.Body.IsDefault && !p.IsDefault {
		_ = h.repo.ClearDefault(nil)
	}
	p.IsDefault = req.Body.IsDefault
	system := p.System
	req.Body.PlanBody.apply(p)
	// apply() writes every settable field. System is not one of them and must not
	// become one: it is the platform's own bookkeeping, not an operator's choice.
	p.System = system
	if err := h.repo.Update(p); err != nil {
		return c.AbortInternalServerError("failed to update plan", err)
	}
	h.record(c, "plan.update", p.ID)
	return ok(c, p)
}

func (h *PlanHandler) Delete(c *okapi.Context) error {
	id := h.id(c)
	// The system plan is not the operator's to remove. workspace.pinUnlimitedPlan
	// resolves it by name and fails soft, so deleting it would not error anywhere
	// — the system workspace would quietly drop onto the default plan's limits and
	// stay there.
	if p, err := h.repo.FindByID(id); err == nil && p.System {
		return c.AbortWithError(409, errSystemPlanDelete)
	}
	n, _ := h.repo.CountWorkspaces(id)
	if n > 0 && c.Query("force") != "true" {
		return c.AbortWithError(409, fmt.Errorf("plan is assigned to %d workspace(s); pass force=true to delete", n))
	}
	if n > 0 {
		if err := h.repo.UnassignAll(id); err != nil {
			return c.AbortInternalServerError("failed to unassign plan", err)
		}
	}
	if err := h.repo.Delete(id); err != nil {
		return c.AbortInternalServerError("failed to delete plan", err)
	}
	h.record(c, "plan.delete", id)
	return message(c, "plan deleted")
}

// SetDefault marks a plan as the default (clearing any previous default).
func (h *PlanHandler) SetDefault(c *okapi.Context) error {
	p, err := h.repo.FindByID(h.id(c))
	if err != nil {
		return c.AbortNotFound("plan not found")
	}
	// Same rule as the update path: the console hides the button, but the endpoint
	// is reachable on its own and a guard in only one of them is not a guard.
	if p.System {
		return c.AbortWithError(409, errSystemPlanDefault)
	}
	_ = h.repo.ClearDefault(nil)
	p.IsDefault = true
	if err := h.repo.Update(p); err != nil {
		return c.AbortInternalServerError("failed to set default plan", err)
	}
	h.record(c, "plan.set_default", p.ID)
	return ok(c, p)
}

// AssignWorkspace sets (or clears) a workspace's plan.
func (h *PlanHandler) AssignWorkspace(c *okapi.Context, req *AssignWorkspacePlanRequest) error {
	wsID, err := strconv.Atoi(c.Param("id"))
	if err != nil || wsID <= 0 {
		return c.AbortBadRequest("invalid workspace id")
	}
	if _, err := h.workspaces.FindByID(uint(wsID)); err != nil {
		return c.AbortNotFound("workspace not found")
	}
	if req.Body.PlanID != nil {
		if _, err := h.repo.FindByID(*req.Body.PlanID); err != nil {
			return c.AbortNotFound("plan not found")
		}
	}
	if err := h.repo.AssignToWorkspace(uint(wsID), req.Body.PlanID); err != nil {
		return c.AbortInternalServerError("failed to assign plan", err)
	}
	h.record(c, "plan.assign", uint(wsID))
	return message(c, "plan assigned")
}

// SetWorkspaceQuotaRequest carries per-workspace limit/capability overrides.
// Each field is nullable: nil inherits the plan, a value overrides it.
type SetWorkspaceQuotaRequest struct {
	Body models.WorkspaceQuota `json:"body"`
}

// GetWorkspaceQuota returns a workspace's overrides (empty = all inherited).
func (h *PlanHandler) GetWorkspaceQuota(c *okapi.Context) error {
	wsID, valid := h.workspaceID(c)
	if !valid {
		return c.AbortBadRequest("invalid workspace id")
	}
	o, err := h.overrides.FindByWorkspace(wsID)
	if err != nil {
		return ok(c, &models.WorkspaceQuota{WorkspaceID: wsID})
	}
	return ok(c, o)
}

// SetWorkspaceQuota upserts a workspace's overrides. Per-workspace overrides are an Enterprise
// capability (gated quota_override → 402 in Community); plans themselves stay free. Reading and
// clearing are ungated, so a license lapse never strands a workspace with an override it can't remove.
func (h *PlanHandler) SetWorkspaceQuota(c *okapi.Context, req *SetWorkspaceQuotaRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagQuotaOverride); err != nil {
		return entitlementAbort(c, err)
	}
	wsID, valid := h.workspaceID(c)
	if !valid {
		return c.AbortBadRequest("invalid workspace id")
	}
	if _, err := h.workspaces.FindByID(wsID); err != nil {
		return c.AbortNotFound("workspace not found")
	}
	// A restricted security-profile override needs the security_profile
	// entitlement, independent of quota_override above (which only unlocks
	// overrides at all).
	if req.Body.SecurityProfile != nil && models.NormalizeSecurityProfile(*req.Body.SecurityProfile) == models.SecurityProfileRestricted {
		if err := h.ee.RequireMutable(enterprise.FlagSecurityProfile); err != nil {
			return entitlementAbort(c, err)
		}
	}
	q := req.Body
	q.WorkspaceID = wsID
	if err := h.overrides.Upsert(&q); err != nil {
		return c.AbortInternalServerError("failed to save overrides", err)
	}
	h.record(c, "plan.override", wsID)
	return ok(c, &q)
}

// DeleteWorkspaceQuota clears a workspace's overrides (falls back to the plan).
func (h *PlanHandler) DeleteWorkspaceQuota(c *okapi.Context) error {
	wsID, valid := h.workspaceID(c)
	if !valid {
		return c.AbortBadRequest("invalid workspace id")
	}
	if err := h.overrides.Delete(wsID); err != nil {
		return c.AbortInternalServerError("failed to clear overrides", err)
	}
	h.record(c, "plan.override.clear", wsID)
	return message(c, "overrides cleared")
}

func (h *PlanHandler) workspaceID(c *okapi.Context) (uint, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return 0, false
	}
	return uint(id), true
}

func (h *PlanHandler) id(c *okapi.Context) uint {
	id, _ := strconv.Atoi(c.Param("id"))
	return uint(id)
}

func (h *PlanHandler) record(c *okapi.Context, action string, targetID uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: action, TargetType: "plan",
		TargetID: strconv.Itoa(int(targetID)), IP: c.RealIP(),
	})
}
