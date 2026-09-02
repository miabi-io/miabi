// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/dnsprovider"
	"github.com/miabi-io/miabi/internal/services/domain"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// DomainHandler exposes workspace-owned domain CRUD and DNS verification.
type DomainHandler struct {
	svc    *domain.Service
	routes *repositories.RouteRepository
	audit  *audit.Logger
}

func NewDomainHandler(svc *domain.Service, routes *repositories.RouteRepository, auditLog *audit.Logger) *DomainHandler {
	return &DomainHandler{svc: svc, routes: routes, audit: auditLog}
}

type CreateDomainRequest struct {
	Body struct {
		Name     string `json:"name" required:"true"`
		TLSMode  string `json:"tls_mode" enum:"acme,custom" default:"acme"`
		Wildcard bool   `json:"wildcard"`
	} `json:"body"`
}

type UpdateDomainRequest struct {
	Body struct {
		Name     string `json:"name"`
		TLSMode  string `json:"tls_mode" enum:"acme,custom"`
		Wildcard bool   `json:"wildcard"`
	} `json:"body"`
}

// domainView augments a domain with its DNS challenge instructions so the UI can
// show the exact TXT record to add. Automated reflects whether a DNS provider is
// linked (Miabi creates the records itself).
type domainView struct {
	*models.Domain
	ChallengeHost  string `json:"challenge_host"`
	ChallengeValue string `json:"challenge_value"`
	Automated      bool   `json:"automated"`
}

type domainRoute struct {
	ID           uint               `json:"id"`
	Name         string             `json:"name"`
	Hosts        []string           `json:"hosts"`
	Status       models.RouteStatus `json:"status"`
	StatusReason string             `json:"status_reason,omitempty"`
	Enabled      bool               `json:"enabled"`
}

type domainDetail struct {
	domainView
	Routes []domainRoute `json:"routes"`
}

func (h *DomainHandler) dependentRoutes(d *models.Domain) []domainRoute {
	out := []domainRoute{}
	if h.routes == nil {
		return out
	}
	routes, err := h.routes.ListByWorkspace(d.WorkspaceID)
	if err != nil {
		return out
	}
	name := strings.ToLower(d.Name)
	for i := range routes {
		rt := &routes[i]
		if !routeHostsUnder(rt.Hosts, name) {
			continue
		}
		out = append(out, domainRoute{
			ID: rt.ID, Name: rt.Name, Hosts: rt.Hosts, Status: rt.Status,
			StatusReason: rt.StatusReason, Enabled: rt.Enabled,
		})
	}
	return out
}

func view(d *models.Domain) domainView {
	return domainView{
		Domain: d, ChallengeHost: d.ChallengeHost(), ChallengeValue: d.ChallengeValue(),
		Automated: d.DNSProviderID != nil,
	}
}

func (h *DomainHandler) List(c *okapi.Context) error {
	items, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list domains", err)
	}
	out := make([]domainView, 0, len(items))
	for i := range items {
		out = append(out, view(&items[i]))
	}
	return ok(c, out)
}

func (h *DomainHandler) Create(c *okapi.Context, req *CreateDomainRequest) error {
	wsID := middlewares.WorkspaceID(c)
	d, err := h.svc.Create(wsID, domain.Input{
		Name: req.Body.Name, TLSMode: models.DomainTLSMode(req.Body.TLSMode), Wildcard: req.Body.Wildcard,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "domain.create", d.ID)
	return created(c, view(d))
}

func (h *DomainHandler) Get(c *okapi.Context) error {
	id, err := uintParam(c, "domainID")
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	d, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	return ok(c, domainDetail{domainView: view(d), Routes: h.dependentRoutes(d)})
}

func (h *DomainHandler) Update(c *okapi.Context, req *UpdateDomainRequest) error {
	id, err := uintParam(c, "domainID")
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	wsID := middlewares.WorkspaceID(c)
	d, err := h.svc.Update(wsID, id, domain.Input{
		Name: req.Body.Name, TLSMode: models.DomainTLSMode(req.Body.TLSMode), Wildcard: req.Body.Wildcard,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "domain.update", d.ID)
	return ok(c, view(d))
}

func (h *DomainHandler) Delete(c *okapi.Context) error {
	id, err := uintParam(c, "domainID")
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(c.Request().Context(), wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "domain.delete", id)
	return message(c, "domain deleted")
}

// SetDNSProviderRequest links (or, with null, unlinks) a DNS provider to a domain.
type SetDNSProviderRequest struct {
	Body struct {
		DNSProviderID *uint `json:"dns_provider_id"` // null = revert to manual
	} `json:"body"`
}

// SetDNSProvider connects a DNS provider to a domain (or clears it).
func (h *DomainHandler) SetDNSProvider(c *okapi.Context, req *SetDNSProviderRequest) error {
	id, err := uintParam(c, "domainID")
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	wsID := middlewares.WorkspaceID(c)
	d, err := h.svc.SetDNSProvider(wsID, id, req.Body.DNSProviderID)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "domain.set_dns_provider", id)
	return ok(c, view(d))
}

// Verify checks the domain's ownership TXT record.
func (h *DomainHandler) Verify(c *okapi.Context) error {
	id, err := uintParam(c, "domainID")
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	wsID := middlewares.WorkspaceID(c)
	d, err := h.svc.Verify(c.Request().Context(), wsID, id)
	if err != nil {
		if errors.Is(err, domain.ErrVerificationFailed) || errors.Is(err, domain.ErrDomainBanned) {
			return c.AbortWithError(409, err)
		}
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "domain.verify", id)
	return ok(c, view(d))
}

func (h *DomainHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action,
		TargetType: "domain", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *DomainHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return c.AbortNotFound("domain not found")
	case errors.Is(err, domain.ErrNameTaken), errors.Is(err, domain.ErrDomainClaimed):
		return c.AbortWithError(409, err)
	case errors.Is(err, domain.ErrNameRequired), errors.Is(err, domain.ErrInvalidName), errors.Is(err, domain.ErrNameImmutable):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, domain.ErrProviderNotFound):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, dnsprovider.ErrConflict):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("domain operation failed", err)
	}
}
