// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
)

// AdminStorageClassHandler manages the disks a platform offers. Every edition may register classes
// up to Entitlements.StorageClassLimit, shared ones included — a Community fleet is node-capped, so
// a shared mount there is not the multi-node story a license guards. The storage_classes
// entitlement lifts the cap. A lapsed license never breaks existing classes: only the next create
// is refused.
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

// overCap reports whether the edition's cap on the whole catalog is already reached. It writes no
// response: entitlementAbort returns nil once it has written one, so the refusal has to be the
// handler's own return or the create runs on regardless. A failed count does not block a create —
// a counting hiccup should not stand between an operator and their disk.
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
	sc, err := h.svc.Get(id)
	if err != nil {
		return c.AbortNotFound("storage class not found")
	}
	return ok(c, sc)
}

// Create registers a class. The node is probed for the path before the row is written, so a class
// whose disk is not mounted fails here instead of at a tenant's first deploy.
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

// Update changes the editable fields. A name or path change is refused: volumes and GitOps
// manifests dereference both, so rewriting one breaks the references instead of moving anything.
// Ungated: an edition that may register a class must be able to correct it, and a class it could
// neither fix nor delete once in use would be a trap.
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

// Delete removes a class no volume references. Ungated, so a lapsed license can still clean up.
// Disabling is the way to stop new volumes landing on a disk without touching the ones already there.
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
