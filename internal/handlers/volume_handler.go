// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"io"
	"path/filepath"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/hostmount"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type VolumeHandler struct {
	svc   *storage.Service
	users *repositories.UserRepository
	audit *audit.Logger
}

func NewVolumeHandler(svc *storage.Service, users *repositories.UserRepository, auditLog *audit.Logger) *VolumeHandler {
	return &VolumeHandler{svc: svc, users: users, audit: auditLog}
}

// VolumeCreateRequest is the body for creating a managed volume.
type VolumeCreateRequest struct {
	Body struct {
		Name     string `json:"name" required:"true"`
		ServerID uint   `json:"server_id"` // node to place on (0 = local)
		SizeMB   int    `json:"size_mb"`   // declared capacity in MB (0 = unspecified)
		// Driver: "local" (default, node-local); "nfs"/"cifs" for shared storage a replicated cluster app
		// can mount across nodes; or "host" to bind an operator-managed host path (privileged workspaces
		// only). DriverOpts are the backend mount options, encrypted at rest and never returned.
		Driver     string            `json:"driver" enum:"local,nfs,cifs,host"`
		DriverOpts map[string]string `json:"driver_opts"`
	} `json:"body"`
}

func (h *VolumeHandler) Create(c *okapi.Context, req *VolumeCreateRequest) error {
	wsID := middlewares.WorkspaceID(c)
	var sizeBytes int64
	if req.Body.SizeMB > 0 {
		sizeBytes = int64(req.Body.SizeMB) * 1024 * 1024
	}
	v, err := h.svc.CreateWith(c.Request().Context(), wsID, req.Body.ServerID, req.Body.Name, sizeBytes, req.Body.Driver, req.Body.DriverOpts, selfOwnerMeta(h.users, c), nil)
	if err != nil {
		if a := quotaAbort(c, err); a != nil {
			return a
		}
		if errors.Is(err, storage.ErrInvalidDriver) || errors.Is(err, storage.ErrDriverDeviceRequired) || errors.Is(err, storage.ErrHostPathRequired) || errors.Is(err, hostmount.ErrInvalidHostPath) {
			return c.AbortBadRequest(err.Error())
		}
		if errors.Is(err, storage.ErrHostMountNotPrivileged) {
			return c.AbortForbidden(err.Error())
		}
		if errors.Is(err, nodes.ErrNodeOffline) || errors.Is(err, node.ErrNodeCordoned) || errors.Is(err, node.ErrNodeNotFound) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to create volume", err)
	}
	h.record(c, wsID, "volume.create", v.ID)
	return created(c, v)
}

func (h *VolumeHandler) List(c *okapi.Context) error {
	vols, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list volumes", err)
	}
	return ok(c, vols)
}

// Storage returns the workspace's declared-vs-measured storage summary. Reads
// cached columns only — no live disk walk.
func (h *VolumeHandler) Storage(c *okapi.Context) error {
	sum, err := h.svc.WorkspaceStorage(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to read storage usage", err)
	}
	return ok(c, sum)
}

// Get returns a volume with its live Docker info and the apps that mount it.
func (h *VolumeHandler) Get(c *okapi.Context) error {
	id, err := strconv.Atoi(c.Param("volumeID"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid volume id")
	}
	detail, err := h.svc.Detail(c.Request().Context(), middlewares.WorkspaceID(c), uint(id))
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	return ok(c, detail)
}

func (h *VolumeHandler) Delete(c *okapi.Context) error {
	v, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	if err := h.svc.Delete(c.Request().Context(), v); err != nil {
		return c.AbortWithError(409, err) // likely in use
	}
	h.record(c, v.WorkspaceID, "volume.delete", v.ID)
	return message(c, "volume deleted")
}

const maxVolumeUploadBytes = 512 << 20 // 512 MiB

// ListFiles returns the files stored in a volume.
func (h *VolumeHandler) ListFiles(c *okapi.Context) error {
	v, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	files, err := h.svc.ListFiles(c.Request().Context(), v)
	if err != nil {
		return c.AbortInternalServerError("failed to list volume files", err)
	}
	return ok(c, files)
}

// UploadFile lands an uploaded file into a volume (multipart: file, path).
func (h *VolumeHandler) UploadFile(c *okapi.Context) error {
	v, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return c.AbortBadRequest("a file is required (field 'file')")
	}
	defer func() { _ = file.Close() }()
	if header.Size > maxVolumeUploadBytes {
		return c.AbortBadRequest("file exceeds the maximum upload size")
	}
	subdir := c.Request().FormValue("path")
	dest, err := h.svc.UploadFile(c.Request().Context(), v, subdir, header.Filename, file, header.Size)
	if err != nil {
		return c.AbortInternalServerError("failed to upload file", err)
	}
	h.record(c, v.WorkspaceID, "volume.upload_file", v.ID)
	return created(c, map[string]any{"path": dest})
}

// DownloadFile streams a single file out of a volume (query: path).
func (h *VolumeHandler) DownloadFile(c *okapi.Context) error {
	v, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	rel := c.Query("path")
	rc, size, err := h.svc.DownloadFile(c.Request().Context(), v, rel)
	if err != nil {
		if errors.Is(err, storage.ErrFileNotFound) {
			return c.AbortNotFound("file not found")
		}
		return c.AbortInternalServerError("failed to download file", err)
	}
	defer func() { _ = rc.Close() }()
	c.SetHeader("Content-Type", "application/octet-stream")
	c.SetHeader("Content-Disposition", `attachment; filename="`+filepath.Base(rel)+`"`)
	if size > 0 {
		c.SetHeader("Content-Length", strconv.FormatInt(size, 10))
	}
	_, _ = io.Copy(c.Response(), rc)
	return nil
}

// DeleteFile removes a file or directory from a volume (query: path).
func (h *VolumeHandler) DeleteFile(c *okapi.Context) error {
	v, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("volume not found")
	}
	if err := h.svc.DeleteFile(c.Request().Context(), v, c.Query("path")); err != nil {
		if errors.Is(err, storage.ErrFileNotFound) {
			return c.AbortNotFound("file not found")
		}
		return c.AbortInternalServerError("failed to delete file", err)
	}
	h.record(c, v.WorkspaceID, "volume.delete_file", v.ID)
	return message(c, "file deleted")
}

func (h *VolumeHandler) load(c *okapi.Context) (*models.Volume, error) {
	id, err := resolveID(c.Param("volumeID"), h.svc.IDByUID)
	if err != nil {
		return nil, errors.New("invalid volume id")
	}
	return h.svc.Get(middlewares.WorkspaceID(c), id)
}

func (h *VolumeHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "volume", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
