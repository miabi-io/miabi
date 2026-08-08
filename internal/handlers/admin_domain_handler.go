// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/domain"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// AdminDomainHandler exposes platform-wide domain administration: a searchable
// list of every workspace's domains, a detail view with the routes that depend on
// each domain, and manual (or forced) ownership validation.
type AdminDomainHandler struct {
	db      *gorm.DB
	domains *repositories.DomainRepository
	routes  *repositories.RouteRepository
	svc     *domain.Service
	audit   *audit.Logger
}

func NewAdminDomainHandler(db *gorm.DB, domains *repositories.DomainRepository, routes *repositories.RouteRepository, svc *domain.Service, auditLog *audit.Logger) *AdminDomainHandler {
	return &AdminDomainHandler{db: db, domains: domains, routes: routes, svc: svc, audit: auditLog}
}

// AdminDomain is a domain row for the admin domains table.
type AdminDomain struct {
	ID            uint                 `json:"id"`
	WorkspaceID   uint                 `json:"workspace_id"`
	WorkspaceName string               `json:"workspace_name"`
	Name          string               `json:"name"`
	Status        models.DomainStatus  `json:"status"`
	Verified      bool                 `json:"verified"`
	VerifiedAt    *time.Time           `json:"verified_at,omitempty"`
	TLSMode       models.DomainTLSMode `json:"tls_mode"`
	Wildcard      bool                 `json:"wildcard"`
	Automated     bool                 `json:"automated"`
	CreatedAt     time.Time            `json:"created_at"`
}

// AdminDomainRoute is a dependent route shown on the domain detail page.
type AdminDomainRoute struct {
	ID           uint               `json:"id"`
	Name         string             `json:"name"`
	Hosts        []string           `json:"hosts"`
	Status       models.RouteStatus `json:"status"`
	StatusReason string             `json:"status_reason,omitempty"`
	Enabled      bool               `json:"enabled"`
}

// AdminDomainDetail enriches a domain with its workspace, challenge instructions,
// dependent routes, and recent activity for the admin detail page.
type AdminDomainDetail struct {
	models.Domain
	WorkspaceName string `json:"workspace_name"`
	// WorkspacePrivileged means the owning workspace may expose routes under this
	// domain even before it is verified (a ban still blocks them).
	WorkspacePrivileged bool               `json:"workspace_privileged"`
	ChallengeHost       string             `json:"challenge_host"`
	ChallengeValue      string             `json:"challenge_value"`
	Automated           bool               `json:"automated"`
	Routes              []AdminDomainRoute `json:"routes"`
	RecentEvents        []models.AuditLog  `json:"recent_events"`
}

// List returns every workspace's domains, filterable by search, status, and
// workspace.
func (h *AdminDomainHandler) List(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	var wsFilter *uint
	if v := queryInt(c, "workspace", 0); v > 0 {
		w := uint(v)
		wsFilter = &w
	}
	domains, total, err := h.domains.ListPagedAll(c.Query("search"), c.Query("status"), wsFilter, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list domains", err)
	}
	names := h.workspaceNames(domains)
	out := make([]AdminDomain, 0, len(domains))
	for i := range domains {
		d := &domains[i]
		out = append(out, AdminDomain{
			ID: d.ID, WorkspaceID: d.WorkspaceID, WorkspaceName: names[d.WorkspaceID],
			Name: d.Name, Status: d.Status(), Verified: d.Verified, VerifiedAt: d.VerifiedAt,
			TLSMode: d.TLSMode, Wildcard: d.Wildcard, Automated: d.DNSProviderID != nil,
			CreatedAt: d.CreatedAt,
		})
	}
	return paginated(c, out, total, page, size)
}

// Get returns a single domain with its workspace, dependent routes, and recent
// activity.
func (h *AdminDomainHandler) Get(c *okapi.Context) error {
	id, err := adminDomainID(c)
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	d, err := h.domains.FindByID(id)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	detail := AdminDomainDetail{
		Domain:         *d,
		ChallengeHost:  d.ChallengeHost(),
		ChallengeValue: d.ChallengeValue(),
		Automated:      d.DNSProviderID != nil,
		Routes:         h.dependentRoutes(d),
		RecentEvents:   []models.AuditLog{},
	}
	var ws models.Workspace
	if err := h.db.First(&ws, d.WorkspaceID).Error; err == nil {
		detail.WorkspaceName = ws.DisplayName
		detail.WorkspacePrivileged = ws.Privileged
	}
	var events []models.AuditLog
	if err := h.db.Where("workspace_id = ? AND target_type = ? AND target_id = ?", d.WorkspaceID, "domain", strconv.Itoa(int(d.ID))).
		Order("created_at DESC").Limit(20).Find(&events).Error; err == nil && len(events) > 0 {
		detail.RecentEvents = events
	}
	return ok(c, detail)
}

// Verify runs the standard DNS ownership check for any workspace's domain.
func (h *AdminDomainHandler) Verify(c *okapi.Context) error {
	id, err := adminDomainID(c)
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	d, err := h.domains.FindByID(id)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	d, err = h.svc.Verify(c.Request().Context(), d.WorkspaceID, d.ID)
	if err != nil {
		if errors.Is(err, domain.ErrVerificationFailed) || errors.Is(err, domain.ErrDomainBanned) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("verification failed", err)
	}
	h.record(c, d.WorkspaceID, "admin.domain.verify", d.ID)
	return ok(c, d)
}

// ForceVerify marks a domain verified without a DNS check (admin override).
func (h *AdminDomainHandler) ForceVerify(c *okapi.Context) error {
	id, err := adminDomainID(c)
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	d, err := h.domains.FindByID(id)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	d, err = h.svc.ForceVerify(c.Request().Context(), d.WorkspaceID, d.ID)
	if err != nil {
		if errors.Is(err, domain.ErrDomainBanned) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("force verify failed", err)
	}
	h.record(c, d.WorkspaceID, "admin.domain.force_verify", d.ID)
	return ok(c, d)
}

// BanDomainRequest carries the optional reason recorded with a ban.
type BanDomainRequest struct {
	Body struct {
		Reason string `json:"reason"`
	} `json:"body"`
}

// Ban blocks a domain platform-wide: its routes are forced offline and it can no
// longer be verified.
func (h *AdminDomainHandler) Ban(c *okapi.Context, req *BanDomainRequest) error {
	id, err := adminDomainID(c)
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	d, err := h.domains.FindByID(id)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	d, err = h.svc.Ban(c.Request().Context(), d.WorkspaceID, d.ID, req.Body.Reason)
	if err != nil {
		return c.AbortInternalServerError("ban failed", err)
	}
	h.record(c, d.WorkspaceID, "admin.domain.ban", d.ID)
	return ok(c, d)
}

// Unban lifts a domain ban so its routes can serve again.
func (h *AdminDomainHandler) Unban(c *okapi.Context) error {
	id, err := adminDomainID(c)
	if err != nil {
		return c.AbortBadRequest("invalid domain id")
	}
	d, err := h.domains.FindByID(id)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	d, err = h.svc.Unban(c.Request().Context(), d.WorkspaceID, d.ID)
	if err != nil {
		return c.AbortInternalServerError("unban failed", err)
	}
	h.record(c, d.WorkspaceID, "admin.domain.unban", d.ID)
	return ok(c, d)
}

// dependentRoutes returns the workspace routes whose hosts fall under the domain,
// with their current config-sync status.
func (h *AdminDomainHandler) dependentRoutes(d *models.Domain) []AdminDomainRoute {
	routes, err := h.routes.ListByWorkspace(d.WorkspaceID)
	if err != nil {
		return []AdminDomainRoute{}
	}
	name := strings.ToLower(d.Name)
	out := []AdminDomainRoute{}
	for i := range routes {
		rt := &routes[i]
		if !routeHostsUnder(rt.Hosts, name) {
			continue
		}
		out = append(out, AdminDomainRoute{
			ID: rt.ID, Name: rt.Name, Hosts: rt.Hosts, Status: rt.Status,
			StatusReason: rt.StatusReason, Enabled: rt.Enabled,
		})
	}
	return out
}

func (h *AdminDomainHandler) workspaceNames(domains []models.Domain) map[uint]string {
	ids := make([]uint, 0, len(domains))
	seen := map[uint]bool{}
	for i := range domains {
		if id := domains[i].WorkspaceID; !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	m := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return m
	}
	var ws []models.Workspace
	h.db.Where("id IN ?", ids).Find(&ws)
	for i := range ws {
		m[ws[i].ID] = ws[i].DisplayName
	}
	return m
}

func (h *AdminDomainHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action,
		TargetType: "domain", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func routeHostsUnder(hosts []string, name string) bool {
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == name || strings.HasSuffix(h, "."+name) {
			return true
		}
	}
	return false
}

func adminDomainID(c *okapi.Context) (uint, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid domain id")
	}
	return uint(id), nil
}
