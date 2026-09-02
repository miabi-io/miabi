// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dnscatalog"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/dnsprovider"
)

// DNSProviderHandler exposes workspace DNS provider connections (connect/test/
// list/delete). Credentials are write-only — never returned after create.
type DNSProviderHandler struct {
	svc   *dnsprovider.Service
	audit *audit.Logger
}

func NewDNSProviderHandler(svc *dnsprovider.Service, auditLog *audit.Logger) *DNSProviderHandler {
	return &DNSProviderHandler{svc: svc, audit: auditLog}
}

type ConnectDNSProviderRequest struct {
	Body struct {
		Name        string            `json:"name" required:"true"` // desired unique slug handle
		DisplayName string            `json:"display_name"`         // free-text label (defaults to name)
		Type        string            `json:"type" required:"true"`
		Credentials map[string]string `json:"credentials"`
		TestZone    string            `json:"test_zone,omitempty"`
	} `json:"body"`
}

type UpdateDNSProviderRequest struct {
	Body struct {
		DisplayName *string           `json:"display_name,omitempty"`
		Credentials map[string]string `json:"credentials,omitempty"`
		TestZone    string            `json:"test_zone,omitempty"`
	} `json:"body"`
}

type TestDNSProviderRequest struct {
	Body struct {
		Zone string `json:"zone" required:"true"` // a domain on the provider to probe
	} `json:"body"`
}

type providerView struct {
	*models.DNSProvider
	CredentialsPublic map[string]string `json:"credentials_public"`
}

func (h *DNSProviderHandler) view(p *models.DNSProvider) providerView {
	return providerView{DNSProvider: p, CredentialsPublic: h.svc.PublicCredentials(p)}
}

func (h *DNSProviderHandler) List(c *okapi.Context) error {
	items, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list DNS providers", err)
	}
	out := make([]providerView, 0, len(items))
	for i := range items {
		out = append(out, h.view(&items[i]))
	}
	return ok(c, out)
}

func (h *DNSProviderHandler) Connect(c *okapi.Context, req *ConnectDNSProviderRequest) error {
	wsID := middlewares.WorkspaceID(c)
	p, err := h.svc.Connect(c.Request().Context(), wsID, dnsprovider.ConnectInput{
		Name: req.Body.Name, DisplayName: req.Body.DisplayName, Type: req.Body.Type,
		Credentials: req.Body.Credentials, TestZone: req.Body.TestZone,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "dns_provider.connect", p.ID)
	return created(c, h.view(p))
}

func (h *DNSProviderHandler) Get(c *okapi.Context) error {
	id, err := uintParam(c, "providerID")
	if err != nil {
		return c.AbortBadRequest("invalid provider id")
	}
	p, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("DNS provider not found")
	}
	return ok(c, p)
}

func (h *DNSProviderHandler) Update(c *okapi.Context, req *UpdateDNSProviderRequest) error {
	id, err := uintParam(c, "providerID")
	if err != nil {
		return c.AbortBadRequest("invalid provider id")
	}
	wsID := middlewares.WorkspaceID(c)
	p, err := h.svc.Update(c.Request().Context(), wsID, id, dnsprovider.UpdateInput{
		DisplayName: req.Body.DisplayName,
		Credentials: req.Body.Credentials,
		TestZone:    req.Body.TestZone,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "dns_provider.update", id)
	return ok(c, h.view(p))
}

// Catalog lists the connectable DNS hosts and the credential fields each needs.
func (h *DNSProviderHandler) Catalog(c *okapi.Context) error {
	return ok(c, dnscatalog.All())
}

func (h *DNSProviderHandler) Test(c *okapi.Context, req *TestDNSProviderRequest) error {
	id, err := uintParam(c, "providerID")
	if err != nil {
		return c.AbortBadRequest("invalid provider id")
	}
	wsID := middlewares.WorkspaceID(c)
	p, err := h.svc.Test(c.Request().Context(), wsID, id, req.Body.Zone)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "dns_provider.test", id)
	return ok(c, p)
}

func (h *DNSProviderHandler) Delete(c *okapi.Context) error {
	id, err := uintParam(c, "providerID")
	if err != nil {
		return c.AbortBadRequest("invalid provider id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "dns_provider.delete", id)
	return message(c, "DNS provider disconnected")
}

func (h *DNSProviderHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action,
		TargetType: "dns_provider", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *DNSProviderHandler) mapErr(c *okapi.Context, err error) error {
	if a := quotaAbort(c, err); a != nil {
		return a
	}
	switch {
	case errors.Is(err, dnsprovider.ErrNotFound):
		return c.AbortNotFound("DNS provider not found")
	case errors.Is(err, dnsprovider.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, dnsprovider.ErrNameRequired), errors.Is(err, dnsprovider.ErrInvalidType),
		errors.Is(err, dnsprovider.ErrInvalidCredentials):
		return c.AbortBadRequest(err.Error())
	default:
		// A failed connection test (bad token / no zone access) is a client error.
		return c.AbortWithError(400, err)
	}
}
