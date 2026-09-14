// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// AdminBrandingHandler owns the operator's identity: the sign-in page, the console
// chrome, and whether accounts may pick their own accent.
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
	// Editable reports whether this licence may CHANGE the branding, which is not
	// the same as whether it may show it. See Update.
	Editable bool `json:"editable"`
	// Accents are the accent codes a brand accent may be set to, so the admin form
	// and the personal picker cannot drift apart.
	Accents []string `json:"accents"`
	// Assets are the uploaded images by slot. An upload takes the place of the
	// matching URL wherever the brand is shown.
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

// Update replaces the branding.
//
// Gated on Mutable rather than Has, unlike Get. An expired licence must not blank
// an operator's sign-in page and put someone else's name on it — that is a support
// call from every user of the install. It stops them editing it instead.
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

// UploadAsset stores an image for a slot (multipart: file), replacing any earlier
// upload. Its type is sniffed from the bytes; see branding.DetectImage.
func (h *AdminBrandingHandler) UploadAsset(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagWhiteLabel); err != nil {
		return c.AbortWithError(402, err)
	}
	slot, err := branding.ParseAssetSlot(c.Param("slot"))
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	// The headroom covers multipart framing. Past it the body is cut off before it is
	// buffered, rather than spooled to disk and refused afterwards.
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

// ServeAsset serves an uploaded image. Public, because the sign-in page showing it
// has no session; gated on Has like the brand in auth status, so expiry keeps it.
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
	// An SVG opened directly, rather than through <img>, would run its scripts on
	// this origin. The sandbox stops that without affecting how it renders as an image.
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
