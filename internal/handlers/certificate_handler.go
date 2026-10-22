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
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/certificate"
	"github.com/miabi-io/miabi/internal/services/managedcert"
)

// CertificateHandler exposes workspace-scoped imported TLS certificates. The
// private key is write-only over the API (accepted on import/replace, never
// returned); only parsed metadata is read back.
type CertificateHandler struct {
	svc     *certificate.Service
	managed *managedcert.Service
	audit   *audit.Logger
}

func NewCertificateHandler(svc *certificate.Service, auditLog *audit.Logger) *CertificateHandler {
	return &CertificateHandler{svc: svc, audit: auditLog}
}

// SetManaged wires the ACME issuance service (nil-safe; nil disables the issue
// endpoint, which then returns 501).
func (h *CertificateHandler) SetManaged(m *managedcert.Service) { h.managed = m }

// IssueCertificateRequest requests a managed (ACME DNS-01) certificate for a
// verified, provider-connected domain.
type IssueCertificateRequest struct {
	Body struct {
		DomainID        uint   `json:"domain_id" required:"true"`
		Name            string `json:"name,omitempty"` // defaults to the domain name
		IncludeWildcard bool   `json:"include_wildcard"`
		AutoRenew       bool   `json:"auto_renew"`
	} `json:"body"`
}

// Issue starts issuing a managed certificate; returns the row in `issuing` state.
func (h *CertificateHandler) Issue(c *okapi.Context, req *IssueCertificateRequest) error {
	if h.managed == nil {
		return c.AbortWithError(501, errors.New("managed certificates are not enabled"))
	}
	wsID := middlewares.WorkspaceID(c)
	cert, err := h.managed.Request(wsID, req.Body.DomainID, req.Body.Name, req.Body.IncludeWildcard, req.Body.AutoRenew)
	if err != nil {
		if a := quotaAbort(c, err); a != nil {
			return a
		}
		switch {
		case errors.Is(err, managedcert.ErrDomainNotFound):
			return c.AbortNotFound("domain not found")
		case errors.Is(err, managedcert.ErrDomainNotVerified), errors.Is(err, managedcert.ErrNoProvider):
			return c.AbortBadRequest(err.Error())
		case errors.Is(err, certificate.ErrNameTaken):
			return c.AbortWithError(409, err)
		default:
			return c.AbortInternalServerError("failed to start issuance", err)
		}
	}
	h.record(c, wsID, "certificate.issue", cert.ID)
	return created(c, cert)
}

type ImportCertificateRequest struct {
	Body struct {
		Name        string `json:"name" required:"true"` // desired unique slug handle
		DisplayName string `json:"display_name"`         // free-text label (defaults to name)
		CertPEM     string `json:"cert_pem" required:"true"`
		KeyPEM      string `json:"key_pem" required:"true"`
	} `json:"body"`
}

type ReplaceCertificateRequest struct {
	Body struct {
		Name    string `json:"name"`
		CertPEM string `json:"cert_pem" required:"true"`
		KeyPEM  string `json:"key_pem" required:"true"`
	} `json:"body"`
}

// List returns the workspace's certificates, or — when ?host= is given — only
// those whose SANs cover that host (for the route form's auto-select).
func (h *CertificateHandler) List(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if host := c.Query("host"); host != "" {
		certs, err := h.svc.MatchHost(wsID, host)
		if err != nil {
			return c.AbortInternalServerError("failed to match certificates", err)
		}
		return ok(c, certs)
	}
	certs, err := h.svc.List(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to list certificates", err)
	}
	return ok(c, certs)
}

func (h *CertificateHandler) Get(c *okapi.Context) error {
	cert, err := h.svc.Get(middlewares.WorkspaceID(c), h.id(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, cert)
}

func (h *CertificateHandler) Usage(c *okapi.Context) error {
	routes, err := h.svc.Usage(middlewares.WorkspaceID(c), h.id(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	out := make([]map[string]any, 0, len(routes))
	for i := range routes {
		out = append(out, map[string]any{"id": routes[i].ID, "name": routes[i].Name})
	}
	return ok(c, out)
}

func (h *CertificateHandler) Import(c *okapi.Context, req *ImportCertificateRequest) error {
	wsID := middlewares.WorkspaceID(c)
	cert, err := h.svc.Import(wsID, req.Body.Name, req.Body.DisplayName, req.Body.CertPEM, req.Body.KeyPEM)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "certificate.import", cert.ID)
	return created(c, cert)
}

func (h *CertificateHandler) Replace(c *okapi.Context, req *ReplaceCertificateRequest) error {
	wsID := middlewares.WorkspaceID(c)
	cert, err := h.svc.Replace(wsID, h.id(c), req.Body.Name, req.Body.CertPEM, req.Body.KeyPEM)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "certificate.replace", cert.ID)
	return ok(c, cert)
}

func (h *CertificateHandler) Delete(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(wsID, h.id(c)); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "certificate.delete", h.id(c))
	return message(c, "certificate deleted")
}

func (h *CertificateHandler) id(c *okapi.Context) uint {
	id, _ := strconv.Atoi(c.Param("certID"))
	return uint(id)
}

func (h *CertificateHandler) mapErr(c *okapi.Context, err error) error {
	if a := quotaAbort(c, err); a != nil {
		return a
	}
	switch {
	case errors.Is(err, certificate.ErrNotFound):
		return c.AbortNotFound("certificate not found")
	case errors.Is(err, certificate.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, certificate.ErrNameRequired):
		return c.AbortBadRequest("a certificate name is required")
	case errors.Is(err, certificate.ErrPEMRequired):
		return c.AbortBadRequest("a certificate and private key are required")
	case errors.Is(err, certificate.ErrInvalidPEM):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, certificate.ErrNoDomains), errors.Is(err, certificate.ErrDomainMismatch):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, certificate.ErrInUse), errors.Is(err, certificate.ErrServesExternalAccess):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("certificate operation failed", err)
	}
}

func (h *CertificateHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "certificate", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
