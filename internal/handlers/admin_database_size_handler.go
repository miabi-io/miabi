// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// AdminDatabaseSizeHandler manages the database sizes a platform offers (Enterprise; gated database_sizes).
type AdminDatabaseSizeHandler struct {
	repo  *repositories.DatabaseSizeRepository
	ee    enterprise.EE
	audit *audit.Logger
}

func NewAdminDatabaseSizeHandler(repo *repositories.DatabaseSizeRepository, ee enterprise.EE, auditLog *audit.Logger) *AdminDatabaseSizeHandler {
	return &AdminDatabaseSizeHandler{repo: repo, ee: ee, audit: auditLog}
}

// databaseSizeBody is the create/update payload. Name is read on create only: instances carry it as a label.
type databaseSizeBody struct {
	Name        string  `json:"name" max:"32"`
	DisplayName string  `json:"display_name" max:"80"`
	Description string  `json:"description" max:"500"`
	MemoryMB    int     `json:"memory_mb" required:"true"`
	CPUCores    float64 `json:"cpu_cores" required:"true"`
}

type CreateDatabaseSizeRequest struct {
	Body databaseSizeBody `json:"body"`
}

type UpdateDatabaseSizeRequest struct {
	Body databaseSizeBody `json:"body"`
}

var (
	errDatabaseSizeName   = errors.New("a size name is lowercase letters, digits and hyphens (max 32), e.g. small")
	errDatabaseSizeLimits = errors.New("a size needs memory and CPU above zero")
)

func (h *AdminDatabaseSizeHandler) List(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagDatabaseSizes); err != nil {
		return entitlementAbort(c, err)
	}
	sizes, err := h.repo.List()
	if err != nil {
		return c.AbortInternalServerError("failed to list database sizes", err)
	}
	return ok(c, sizes)
}

func (h *AdminDatabaseSizeHandler) Create(c *okapi.Context, req *CreateDatabaseSizeRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagDatabaseSizes); err != nil {
		return entitlementAbort(c, err)
	}
	name := strings.TrimSpace(req.Body.Name)
	if !models.ValidDatabaseSizeName(name) {
		return c.AbortBadRequest(errDatabaseSizeName.Error())
	}
	if _, err := h.repo.FindByName(name); err == nil {
		return c.AbortWithError(409, fmt.Errorf("a database size named %q already exists", name))
	}
	sz := &models.DatabaseSize{Name: name}
	if err := applyDatabaseSize(sz, req.Body); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.repo.Create(sz); err != nil {
		return c.AbortInternalServerError("failed to create the database size", err)
	}
	h.record(c, "admin.database_size.create", sz.ID)
	return created(c, sz)
}

// Update changes a size for the databases given it from now on; instances already on it keep their limits.
func (h *AdminDatabaseSizeHandler) Update(c *okapi.Context, req *UpdateDatabaseSizeRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagDatabaseSizes); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid database size id")
	}
	sz, err := h.repo.FindByID(id)
	if err != nil {
		return c.AbortNotFound("database size not found")
	}
	if err := applyDatabaseSize(sz, req.Body); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.repo.Update(sz); err != nil {
		return c.AbortInternalServerError("failed to update the database size", err)
	}
	h.record(c, "admin.database_size.update", sz.ID)
	return ok(c, sz)
}

// Delete removes a size no plan or workspace override offers. Ungated, so a lapsed license can still clean up;
// instances sized with it keep their limits and its name as a label.
func (h *AdminDatabaseSizeHandler) Delete(c *okapi.Context) error {
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid database size id")
	}
	if _, err := h.repo.FindByID(id); err != nil {
		return c.AbortNotFound("database size not found")
	}
	plans, workspaces, err := h.repo.OfferedBy(id)
	if err != nil {
		return c.AbortInternalServerError("failed to check where the size is offered", err)
	}
	if len(plans) > 0 || len(workspaces) > 0 {
		return c.AbortWithError(409, fmt.Errorf("the size is offered by %s; remove it there first", offeredByText(plans, len(workspaces))))
	}
	if err := h.repo.Delete(id); err != nil {
		return c.AbortInternalServerError("failed to delete the database size", err)
	}
	h.record(c, "admin.database_size.delete", id)
	return message(c, "database size deleted")
}

func applyDatabaseSize(sz *models.DatabaseSize, b databaseSizeBody) error {
	memory := int64(b.MemoryMB) * 1024 * 1024
	cpu := int64(math.Round(b.CPUCores * 1e9))
	if memory <= 0 || cpu <= 0 {
		return errDatabaseSizeLimits
	}
	sz.DisplayName = strings.TrimSpace(b.DisplayName)
	sz.Description = strings.TrimSpace(b.Description)
	sz.MemoryBytes, sz.NanoCPUs = memory, cpu
	return nil
}

func offeredByText(plans []string, workspaces int) string {
	var parts []string
	if len(plans) > 0 {
		parts = append(parts, "the plans "+strings.Join(plans, ", "))
	}
	if workspaces > 0 {
		parts = append(parts, fmt.Sprintf("%d workspace overrides", workspaces))
	}
	return strings.Join(parts, " and ")
}

func (h *AdminDatabaseSizeHandler) record(c *okapi.Context, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: action, TargetType: "database_size",
		TargetID: strconv.Itoa(int(id)), IP: c.RealIP(),
	})
}
