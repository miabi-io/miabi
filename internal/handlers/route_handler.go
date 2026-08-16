// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/settings"
)

type RouteHandler struct {
	svc      *route.Service
	settings *settings.Provider
	audit    *audit.Logger
}

func NewRouteHandler(svc *route.Service, settingsProvider *settings.Provider, auditLog *audit.Logger) *RouteHandler {
	return &RouteHandler{svc: svc, settings: settingsProvider, audit: auditLog}
}

func (h *RouteHandler) externalConfig() route.ExternalConfig {
	return route.ExternalConfig{
		BaseDomain: h.settings.String(settings.KeyExternalBaseDomain, ""),
		Provider:   h.settings.String(settings.KeyExternalBaseProvider, ""),
	}
}

type CreateRouteRequest struct {
	Body struct {
		Name              string                   `json:"name" required:"true"` // unique slug handle (Goma route name)
		DisplayName       string                   `json:"display_name"`         // free-text label (defaults to name)
		ApplicationID     uint                     `json:"application_id" required:"true"`
		Path              string                   `json:"path"`
		Hosts             []string                 `json:"hosts"`
		Methods           []string                 `json:"methods"`
		Middlewares       []string                 `json:"middlewares"`
		Rewrite           string                   `json:"rewrite"`
		TargetPort        int                      `json:"target_port"`
		TLSMode           string                   `json:"tls_mode" enum:"none,acme,custom"`
		AdvancedConfig    string                   `json:"advanced_config"`
		CertificateID     *uint                    `json:"certificate_id"`
		Enabled           *bool                    `json:"enabled"`
		ExploitProtection *bool                    `json:"exploit_protection"`
		Maintenance       *models.RouteMaintenance `json:"maintenance"`
	} `json:"body"`
}

type UpdateRouteRequest struct {
	Body struct {
		Name              string                   `json:"name"`
		Path              string                   `json:"path"`
		Hosts             []string                 `json:"hosts"`
		Methods           []string                 `json:"methods"`
		Middlewares       []string                 `json:"middlewares"`
		Rewrite           string                   `json:"rewrite"`
		TargetPort        int                      `json:"target_port"`
		TLSMode           string                   `json:"tls_mode" enum:"none,acme,custom"`
		AdvancedConfig    string                   `json:"advanced_config"`
		CertificateID     *uint                    `json:"certificate_id"`
		Enabled           *bool                    `json:"enabled"`
		ExploitProtection *bool                    `json:"exploit_protection"`
		Maintenance       *models.RouteMaintenance `json:"maintenance"`
	} `json:"body"`
}

func (h *RouteHandler) Create(c *okapi.Context, req *CreateRouteRequest) error {
	wsID := middlewares.WorkspaceID(c)
	rt, err := h.svc.Create(c.Request().Context(), wsID, route.Input{
		Name: req.Body.Name, DisplayName: req.Body.DisplayName, ApplicationID: req.Body.ApplicationID, Path: req.Body.Path,
		Hosts: req.Body.Hosts, Methods: req.Body.Methods, Middlewares: req.Body.Middlewares,
		Rewrite: req.Body.Rewrite, TargetPort: req.Body.TargetPort, TLSMode: models.RouteTLSMode(req.Body.TLSMode),
		AdvancedConfig: req.Body.AdvancedConfig, CertificateID: req.Body.CertificateID,
		Enabled:           req.Body.Enabled,
		ExploitProtection: req.Body.ExploitProtection, Maintenance: req.Body.Maintenance,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "route.create", rt.ID)
	return created(c, rt)
}

func (h *RouteHandler) List(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if appQ := c.Query("application_id"); appQ != "" {
		appID, _ := strconv.Atoi(appQ)
		routes, err := h.svc.ListByApp(wsID, uint(appID))
		if err != nil {
			return c.AbortInternalServerError("failed to list routes", err)
		}
		return ok(c, routes)
	}
	routes, err := h.svc.List(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to list routes", err)
	}
	return ok(c, routes)
}

func (h *RouteHandler) Get(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid route id")
	}
	rt, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("route not found")
	}
	return ok(c, rt)
}

func (h *RouteHandler) Update(c *okapi.Context, req *UpdateRouteRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid route id")
	}
	wsID := middlewares.WorkspaceID(c)
	if h.guardGenerated(c, wsID, id) {
		return nil // 409 already written
	}
	rt, err := h.svc.Update(c.Request().Context(), wsID, id, route.Input{
		Name: req.Body.Name, Path: req.Body.Path, Hosts: req.Body.Hosts, Methods: req.Body.Methods,
		Middlewares: req.Body.Middlewares, Rewrite: req.Body.Rewrite, TargetPort: req.Body.TargetPort,
		TLSMode: models.RouteTLSMode(req.Body.TLSMode), AdvancedConfig: req.Body.AdvancedConfig,
		CertificateID: req.Body.CertificateID, Enabled: req.Body.Enabled,
		ExploitProtection: req.Body.ExploitProtection,
		Maintenance:       req.Body.Maintenance,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "route.update", rt.ID)
	return ok(c, rt)
}

type SetRouteEnabledRequest struct {
	Body struct {
		Enabled bool `json:"enabled"`
	} `json:"body"`
}

// SetEnabled flips only the route's enabled flag (partial update), leaving its
// hosts/methods/middlewares/etc. untouched.
func (h *RouteHandler) SetEnabled(c *okapi.Context, req *SetRouteEnabledRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid route id")
	}
	wsID := middlewares.WorkspaceID(c)
	if h.guardGenerated(c, wsID, id) {
		return nil // 409 already written
	}
	rt, err := h.svc.SetEnabled(c.Request().Context(), wsID, id, req.Body.Enabled)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "route.update", rt.ID)
	return ok(c, rt)
}

type SetRouteSecurityRequest struct {
	Body struct {
		ExploitProtection *bool `json:"exploit_protection"`
	} `json:"body"`
}

func (h *RouteHandler) SetSecurity(c *okapi.Context, req *SetRouteSecurityRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid route id")
	}
	wsID := middlewares.WorkspaceID(c)
	rt, err := h.svc.SetSecurity(c.Request().Context(), wsID, id, req.Body.ExploitProtection)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "route.update", rt.ID)
	return ok(c, rt)
}

type SetRouteMaintenanceRequest struct {
	Body struct {
		Enabled    bool   `json:"enabled"`
		StatusCode int    `json:"status_code"`
		Message    string `json:"message"`
	} `json:"body"`
}

func (h *RouteHandler) SetMaintenance(c *okapi.Context, req *SetRouteMaintenanceRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid route id")
	}
	wsID := middlewares.WorkspaceID(c)
	rt, err := h.svc.SetMaintenance(c.Request().Context(), wsID, id, models.RouteMaintenance{
		Enabled: req.Body.Enabled, StatusCode: req.Body.StatusCode, Message: req.Body.Message,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	action := "route.maintenance.off"
	if rt.Maintenance.Enabled {
		action = "route.maintenance.on"
	}
	h.record(c, wsID, action, rt.ID)
	return ok(c, rt)
}

// AttachRouteMiddlewareRequest names a workspace middleware to attach to a route.
type AttachRouteMiddlewareRequest struct {
	Body struct {
		Name string `json:"name" required:"true"`
	} `json:"body"`
}

// AttachMiddleware adds a workspace middleware to a route's chain. Unlike Update it does NOT
// block generated routes: layering middleware onto a platform-generated external-access route is
// an explicitly supported flow, so users can secure auto-generated routes without editing them.
func (h *RouteHandler) AttachMiddleware(c *okapi.Context, req *AttachRouteMiddlewareRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid route id")
	}
	wsID := middlewares.WorkspaceID(c)
	rt, err := h.svc.AttachMiddleware(c.Request().Context(), wsID, id, req.Body.Name)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "route.update", rt.ID)
	return ok(c, rt)
}

// SetRouteMiddlewaresRequest carries the whole chain, in execution order.
type SetRouteMiddlewaresRequest struct {
	Body struct {
		Middlewares []string `json:"middlewares"`
	} `json:"body"`
}

// SetMiddlewares replaces the route's chain in the order given. The gateway runs
// middlewares in array order, so this is the ordering control, not a bulk
// attach. Allowed on generated routes, mirroring AttachMiddleware.
func (h *RouteHandler) SetMiddlewares(c *okapi.Context, req *SetRouteMiddlewaresRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid route id")
	}
	wsID := middlewares.WorkspaceID(c)
	rt, err := h.svc.SetMiddlewares(c.Request().Context(), wsID, id, req.Body.Middlewares)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "route.update", rt.ID)
	return ok(c, rt)
}

// DetachMiddleware removes a middleware (named in the path) from a route's chain.
// Idempotent and allowed on generated routes, mirroring AttachMiddleware.
func (h *RouteHandler) DetachMiddleware(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid route id")
	}
	wsID := middlewares.WorkspaceID(c)
	rt, err := h.svc.DetachMiddleware(c.Request().Context(), wsID, id, c.Param("name"))
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "route.update", rt.ID)
	return ok(c, rt)
}

func (h *RouteHandler) Delete(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid route id")
	}
	wsID := middlewares.WorkspaceID(c)
	if h.guardGenerated(c, wsID, id) {
		return nil // 409 already written
	}
	if err := h.svc.Delete(c.Request().Context(), wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "route.delete", id)
	return message(c, "route deleted")
}

// guardGenerated blocks user edits and deletes of a platform-generated external-access route.
// These are reconciled by the platform from the application's External Access card. Writes a 409
// and returns true when blocked; a failed lookup is ignored so the op surfaces its own not-found.
func (h *RouteHandler) guardGenerated(c *okapi.Context, wsID, id uint) bool {
	rt, err := h.svc.Get(wsID, id)
	if err != nil {
		return false
	}
	if rt.Generated {
		_ = c.AbortWithError(409, errors.New("this route is auto-generated for external access; manage it from the application's External Access"))
		return true
	}
	return false
}

func (h *RouteHandler) id(c *okapi.Context) (uint, error) {
	id, err := resolveID(c.Param("routeID"), h.svc.IDByUID)
	if err != nil {
		return 0, errors.New("invalid route id")
	}
	return id, nil
}

func (h *RouteHandler) appID(c *okapi.Context) (uint, error) {
	id, err := strconv.Atoi(c.Param("appID"))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid application id")
	}
	return uint(id), nil
}

// ExternalAccess returns an app's external-access state (feature enabled,
// subdomain label, currently exposed ports + URLs).
func (h *RouteHandler) ExternalAccess(c *okapi.Context) error {
	appID, err := h.appID(c)
	if err != nil {
		return c.AbortBadRequest("invalid application id")
	}
	out, err := h.svc.GetExternalAccess(middlewares.WorkspaceID(c), appID, h.externalConfig())
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, out)
}

// SetExternalAccessRequest selects which container ports to expose externally.
type SetExternalAccessRequest struct {
	Body struct {
		Ports []int `json:"ports"`
	} `json:"body"`
}

// SetExternalAccess reconciles the app's externally-exposed ports: generates a
// public hostname + Gateway route per selected port and removes the rest.
func (h *RouteHandler) SetExternalAccess(c *okapi.Context, req *SetExternalAccessRequest) error {
	appID, err := h.appID(c)
	if err != nil {
		return c.AbortBadRequest("invalid application id")
	}
	wsID := middlewares.WorkspaceID(c)
	out, err := h.svc.SetExternalAccess(c.Request().Context(), wsID, appID, req.Body.Ports, h.externalConfig())
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "route.external_access", appID)
	return ok(c, out)
}

func (h *RouteHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "route", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *RouteHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, route.ErrNameTaken), errors.Is(err, route.ErrHostTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, route.ErrNameRequired), errors.Is(err, route.ErrInvalidName), errors.Is(err, route.ErrCertRequired), errors.Is(err, route.ErrInvalidYAML), errors.Is(err, route.ErrDomainNotRegistered), errors.Is(err, route.ErrDomainBanned), errors.Is(err, route.ErrAdvancedTLSCert):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, route.ErrAppRequired), errors.Is(err, route.ErrNodeAddressRequired), errors.Is(err, route.ErrExternalAccessDisabled), errors.Is(err, route.ErrMiddlewareRequired):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, route.ErrMiddlewareDuplicate), errors.Is(err, route.ErrMaintenanceStatus), errors.Is(err, route.ErrMaintenanceMessage):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, route.ErrMiddlewareNotFound):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, route.ErrNotFound):
		return c.AbortNotFound("route not found")
	default:
		return c.AbortInternalServerError("route operation failed", err)
	}
}
