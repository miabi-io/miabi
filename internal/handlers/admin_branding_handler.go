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
	"io"
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/branding"
)

type AdminBrandingHandler struct {
	svc   *branding.Service
	ee    enterprise.EE
	audit *audit.Logger
}

func NewAdminBrandingHandler(svc *branding.Service, ee enterprise.EE, auditLog *audit.Logger) *AdminBrandingHandler {
	return &AdminBrandingHandler{svc: svc, ee: ee, audit: auditLog}
}

// BrandingResponse is the stored branding plus what the console may do with it.
type BrandingResponse struct {
	branding.Branding
	// Editable is whether the licence may change branding, not whether it may show it.
	Editable bool `json:"editable"`
	// Shared so the admin form and the personal picker cannot drift apart.
	Accents []string `json:"accents"`
	// An upload takes the place of the matching URL wherever the brand is shown.
	Assets         map[branding.AssetSlot]branding.Asset `json:"assets"`
	MaxAssetBytes  int                                   `json:"max_asset_bytes"`
	MaxNoticeRunes int                                   `json:"max_notice_runes"`
}

func (h *AdminBrandingHandler) response(editable bool) BrandingResponse {
	return BrandingResponse{
		Branding:       h.svc.Get(),
		Editable:       editable,
		Accents:        models.AccentCodes(),
		Assets:         h.svc.Assets(),
		MaxAssetBytes:  branding.MaxAssetBytes,
		MaxNoticeRunes: branding.MaxNoticeRunes,
	}
}

// Get returns the current branding.
func (h *AdminBrandingHandler) Get(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagWhiteLabel); err != nil {
		return c.AbortWithError(402, err)
	}
	return ok(c, h.response(h.ee.Mutable(enterprise.FlagWhiteLabel)))
}

// UpdateBrandingRequest is the admin form's body.
type UpdateBrandingRequest struct {
	Body struct {
		Name         string          `json:"name"`
		LogoURL      string          `json:"logo_url"`
		LogoDarkURL  string          `json:"logo_dark_url"`
		Accent       string          `json:"accent"`
		AccentPolicy string          `json:"accent_policy"`
		SigninNotice string          `json:"signin_notice"`
		Links        []branding.Link `json:"links"`
	} `json:"body"`
}

// Update gates on Mutable, not Has, so an expired licence freezes branding rather than blanking the sign-in page.
func (h *AdminBrandingHandler) Update(c *okapi.Context, req *UpdateBrandingRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagWhiteLabel); err != nil {
		return c.AbortWithError(402, err)
	}
	in := branding.Branding{
		Name:         req.Body.Name,
		LogoURL:      req.Body.LogoURL,
		LogoDarkURL:  req.Body.LogoDarkURL,
		Accent:       models.Accent(req.Body.Accent),
		AccentPolicy: branding.AccentPolicy(req.Body.AccentPolicy),
		SigninNotice: req.Body.SigninNotice,
		Links:        req.Body.Links,
	}
	if err := h.svc.Save(in); err != nil {
		switch {
		case errors.Is(err, branding.ErrUnsupportedScheme),
			errors.Is(err, branding.ErrTooManyLinks),
			errors.Is(err, branding.ErrLabelTooLong),
			errors.Is(err, branding.ErrLabelRequired),
			errors.Is(err, branding.ErrInvalidAccent),
			errors.Is(err, branding.ErrInvalidAccentPolicy),
			errors.Is(err, branding.ErrNoticeTooLong):
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to save branding", err)
	}
	h.record(c, "admin.branding_update", strconv.Itoa(int(middlewares.UserID(c))))
	return ok(c, h.response(true))
}

func (h *AdminBrandingHandler) UploadAsset(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagWhiteLabel); err != nil {
		return c.AbortWithError(402, err)
	}
	slot, err := branding.ParseAssetSlot(c.Param("slot"))
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	// Cut the body off before it is buffered rather than spooling to disk and refusing afterwards.
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, branding.MaxAssetBytes+64<<10)
	file, _, err := c.Request().FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return c.AbortBadRequest(branding.ErrAssetTooLarge.Error())
		}
		return c.AbortBadRequest("an image is required (field 'file')")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, branding.MaxAssetBytes+1))
	if err != nil {
		return c.AbortBadRequest("failed to read the uploaded image")
	}
	if err := h.svc.PutAsset(slot, data); err != nil {
		if errors.Is(err, branding.ErrAssetTooLarge) || errors.Is(err, branding.ErrUnsupportedImage) {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to store the image", err)
	}
	h.record(c, "admin.branding_asset_upload", string(slot))
	return ok(c, h.response(true))
}

// DeleteAsset removes a slot's upload.
func (h *AdminBrandingHandler) DeleteAsset(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagWhiteLabel); err != nil {
		return c.AbortWithError(402, err)
	}
	slot, err := branding.ParseAssetSlot(c.Param("slot"))
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.svc.DeleteAsset(slot); err != nil {
		return c.AbortInternalServerError("failed to remove the image", err)
	}
	h.record(c, "admin.branding_asset_delete", string(slot))
	return ok(c, h.response(true))
}

// ServeAsset is public because the sign-in page has no session; gated on Has so expiry keeps it.
func (h *AdminBrandingHandler) ServeAsset(c *okapi.Context) error {
	slot, err := branding.ParseAssetSlot(c.Param("slot"))
	if err != nil || !h.ee.Has(enterprise.FlagWhiteLabel) {
		return c.AbortNotFound("image not found")
	}
	a, err := h.svc.OpenAsset(slot)
	if err != nil {
		if errors.Is(err, branding.ErrAssetNotFound) {
			return c.AbortNotFound("image not found")
		}
		return c.AbortInternalServerError("failed to read the image", err)
	}
	etag := `"` + a.SHA256 + `"`
	c.SetHeader("ETag", etag)
	c.SetHeader("Cache-Control", branding.AssetCacheControl(c.Query("v"), a.SHA256))
	c.SetHeader("X-Content-Type-Options", "nosniff")
	// An SVG opened directly would run scripts on this origin; the sandbox stops that.
	c.SetHeader("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	if c.Request().Header.Get("If-None-Match") == etag {
		c.WriteStatus(http.StatusNotModified)
		return nil
	}
	return c.Data(http.StatusOK, a.ContentType, a.Data)
}

func (h *AdminBrandingHandler) record(c *okapi.Context, action, target string) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: action,
		TargetType: "branding", TargetID: target, IP: c.RealIP(),
	})
}
