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
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/storageclass"
	"gorm.io/gorm"
)

// A lapsed license never breaks existing classes: only the next create is refused.
type AdminStorageClassHandler struct {
	svc   *storageclass.Service
	ee    enterprise.EE
	audit *audit.Logger
}

func NewAdminStorageClassHandler(svc *storageclass.Service, ee enterprise.EE, auditLog *audit.Logger) *AdminStorageClassHandler {
	return &AdminStorageClassHandler{svc: svc, ee: ee, audit: auditLog}
}

type storageClassBody struct {
	Name          string `json:"name" maxLength:"32"`
	DisplayName   string `json:"display_name" maxLength:"120"`
	Description   string `json:"description" maxLength:"500"`
	ServerID      uint   `json:"server_id"`
	ClusterID     uint   `json:"cluster_id"`
	Path          string `json:"path" maxLength:"512"`
	Shared        bool   `json:"shared"`
	IsDefault     bool   `json:"is_default"`
	Enabled       bool   `json:"enabled"`
	ReclaimPolicy string `json:"reclaim_policy" enum:"delete,retain"`
}

type CreateStorageClassRequest struct {
	Body storageClassBody `json:"body"`
}

type UpdateStorageClassRequest struct {
	Body storageClassBody `json:"body"`
}

// Writes no response: entitlementAbort returns nil, so the refusal must be the handler's own return.
func (h *AdminStorageClassHandler) overCap() bool {
	if h.ee.Mutable(enterprise.FlagStorageClasses) {
		return false
	}
	n, err := h.svc.Count()
	return err == nil && n >= int64(enterprise.CommunityStorageClassLimit)
}

func (h *AdminStorageClassHandler) List(c *okapi.Context) error {
	classes, err := h.svc.List()
	if err != nil {
		return c.AbortInternalServerError("failed to list storage classes", err)
	}
	return ok(c, classes)
}

func (h *AdminStorageClassHandler) Get(c *okapi.Context) error {
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid storage class id")
	}
	sc, err := h.svc.Detail(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.AbortNotFound("storage class not found")
		}
		return c.AbortInternalServerError("failed to load storage class", err)
	}
	return ok(c, sc)
}

// The node is probed for the path first, so an unmounted disk fails here, not at a tenant's first deploy.
func (h *AdminStorageClassHandler) Create(c *okapi.Context, req *CreateStorageClassRequest) error {
	if h.overCap() {
		return entitlementAbort(c, enterprise.ErrStorageClassLimit)
	}
	sc, err := h.svc.Create(c.Request().Context(), storageClassInput(req.Body))
	if err != nil {
		return storageClassAbort(c, err)
	}
	h.record(c, "admin.storage_class.create", sc.ID)
	return created(c, sc)
}

// Name and path are immutable: volumes and GitOps manifests dereference both.
func (h *AdminStorageClassHandler) Update(c *okapi.Context, req *UpdateStorageClassRequest) error {
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid storage class id")
	}
	sc, err := h.svc.Update(id, storageClassInput(req.Body))
	if err != nil {
		return storageClassAbort(c, err)
	}
	h.record(c, "admin.storage_class.update", sc.ID)
	return ok(c, sc)
}

// Ungated, so a lapsed license can still clean up.
func (h *AdminStorageClassHandler) Delete(c *okapi.Context) error {
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid storage class id")
	}
	if err := h.svc.Delete(id); err != nil {
		return storageClassAbort(c, err)
	}
	h.record(c, "admin.storage_class.delete", id)
	return message(c, "storage class deleted")
}

func storageClassInput(b storageClassBody) storageclass.Input {
	return storageclass.Input{
		Name: b.Name, DisplayName: b.DisplayName, Description: b.Description,
		ServerID: b.ServerID, ClusterID: b.ClusterID, Path: b.Path,
		Shared: b.Shared, IsDefault: b.IsDefault, Enabled: b.Enabled,
		ReclaimPolicy: models.ReclaimPolicy(b.ReclaimPolicy),
	}
}

func storageClassAbort(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, storageclass.ErrImmutable), errors.Is(err, storageclass.ErrBuiltin),
		errors.Is(err, storageclass.ErrInUse), errors.Is(err, storageclass.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, models.ErrStorageClassPath), errors.Is(err, models.ErrStorageClassName),
		errors.Is(err, storageclass.ErrNameRequired),
		errors.Is(err, storageclass.ErrPathRequired), errors.Is(err, storageclass.ErrPathMissing):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, storageclass.ErrNotFound):
		return c.AbortNotFound(err.Error())
	default:
		return c.AbortInternalServerError("storage class operation failed", err)
	}
}

func (h *AdminStorageClassHandler) record(c *okapi.Context, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: action, TargetType: "storage_class",
		TargetID: strconv.Itoa(int(id)), IP: c.RealIP(),
	})
}
