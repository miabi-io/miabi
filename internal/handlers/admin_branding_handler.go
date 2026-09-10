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
	"github.com/miabi-io/miabi/internal/services/branding"
)

// AdminBrandingHandler owns the operator's identity on the sign-in page.
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
}

// Get returns the current branding.
func (h *AdminBrandingHandler) Get(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagWhiteLabel); err != nil {
		return c.AbortWithError(402, err)
	}
	return ok(c, BrandingResponse{
		Branding: h.svc.Get(),
		Editable: h.ee.Mutable(enterprise.FlagWhiteLabel),
		Accents:  models.AccentCodes(),
	})
}

// UpdateBrandingRequest is the admin form's body.
type UpdateBrandingRequest struct {
	Body struct {
		Name    string          `json:"name"`
		LogoURL string          `json:"logo_url"`
		Accent  string          `json:"accent"`
		Links   []branding.Link `json:"links"`
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
		Name:    req.Body.Name,
		LogoURL: req.Body.LogoURL,
		Accent:  models.Accent(req.Body.Accent),
		Links:   req.Body.Links,
	}
	if err := h.svc.Save(in); err != nil {
		switch {
		case errors.Is(err, branding.ErrUnsupportedScheme),
			errors.Is(err, branding.ErrTooManyLinks),
			errors.Is(err, branding.ErrLabelTooLong),
			errors.Is(err, branding.ErrLabelRequired),
			errors.Is(err, branding.ErrInvalidAccent):
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to save branding", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: "admin.branding_update",
		TargetType: "branding", TargetID: strconv.Itoa(int(actor)), IP: c.RealIP(),
	})
	return ok(c, BrandingResponse{
		Branding: h.svc.Get(),
		Editable: true,
		Accents:  models.AccentCodes(),
	})
}
