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
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/billing"
)

// BillingHandler serves the Billing API (/api/v1/billing) to a billing key.
type BillingHandler struct {
	svc   *billing.Service
	audit *audit.Logger
}

// BillingPlan is the contract's Plan.
type BillingPlan = billing.Plan

// NewBillingHandler builds the handler.
func NewBillingHandler(svc *billing.Service, auditLogger *audit.Logger) *BillingHandler {
	return &BillingHandler{svc: svc, audit: auditLogger}
}

// Plans lists the plans a billing platform can map its packages to.
func (h *BillingHandler) Plans(c *okapi.Context) error {
	plans, err := h.svc.Plans()
	if err != nil {
		return c.AbortInternalServerError("failed to list plans", err)
	}
	return ok(c, plans)
}

// PlanSchema lists the plan fields this Miabi accepts from a console.
func (h *BillingHandler) PlanSchema(c *okapi.Context) error {
	sc, err := h.svc.PlanSchema()
	if err != nil {
		return c.AbortInternalServerError("failed to describe plans", err)
	}
	return ok(c, sc)
}

// BillingPushPlanRequest is PUT /plans/{key}.
type BillingPushPlanRequest struct {
	Key    string           `path:"key" required:"true" doc:"The console's stable plan key"`
	DryRun bool             `query:"dry_run"`
	Body   billing.PlanPush `json:"body"`
}

// PushPlan creates or updates a console-managed plan.
func (h *BillingHandler) PushPlan(c *okapi.Context, req *BillingPushPlanRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		res, dry, err := h.svc.PushPlan(req.Key, req.Body, req.DryRun)
		if err != nil {
			return nil, err
		}
		if dry != nil {
			return dry, nil
		}
		h.record(c, "billing.plan.push", "", map[string]any{"key": req.Key, "name": req.Body.Name, "created": res.Created, "adopted": res.Adopted, "ignored": res.Ignored})
		return res, nil
	})
}

// BillingPlanRef names a console plan in the path.
type BillingPlanRef struct {
	Key string `path:"key" required:"true"`
}

// RemovePlan withdraws a console-managed plan.
func (h *BillingHandler) RemovePlan(c *okapi.Context, req *BillingPlanRef) error {
	return h.write(c, struct{}{}, func() (any, error) {
		deleted, err := h.svc.RemovePlan(req.Key)
		if err != nil {
			return nil, err
		}
		h.record(c, "billing.plan.remove", "", map[string]any{"key": req.Key, "deleted": deleted})
		return map[string]any{"key": req.Key, "deleted": deleted}, nil
	})
}

// BillingAccountRef names an account in the path.
type BillingAccountRef struct {
	ExternalID string `path:"external_id" required:"true" doc:"The billing platform's customer id"`
}

// BillingUpsertAccountRequest is PUT /accounts/{external_id}.
type BillingUpsertAccountRequest struct {
	ExternalID string                `path:"external_id" required:"true"`
	Body       billing.AccountUpsert `json:"body"`
}

// BillingPushSubscriptionRequest is PUT /accounts/{external_id}/subscription.
type BillingPushSubscriptionRequest struct {
	ExternalID string                   `path:"external_id" required:"true"`
	DryRun     bool                     `query:"dry_run" doc:"Report the limits the change would break, without applying it"`
	Body       billing.SubscriptionPush `json:"body"`
}

// GetAccount returns an account.
func (h *BillingHandler) GetAccount(c *okapi.Context, req *BillingAccountRef) error {
	a, err := h.svc.GetAccount(req.ExternalID)
	if err != nil {
		return billingErr(c, err, "failed to read the billing account")
	}
	return ok(c, a)
}

// UpsertAccount creates or updates an account.
func (h *BillingHandler) UpsertAccount(c *okapi.Context, req *BillingUpsertAccountRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		a, err := h.svc.UpsertAccount(c.Request().Context(), req.ExternalID, req.Body)
		if err == nil {
			h.record(c, "billing.account.upsert", req.ExternalID, map[string]any{"owner_email": strings.ToLower(req.Body.Owner.Email),
				"create_workspace": req.Body.CreateWorkspace != nil, "max_workspaces": req.Body.MaxWorkspaces})
		}
		return a, err
	})
}

// CancelAccount cancels an account; its workspaces are kept.
func (h *BillingHandler) CancelAccount(c *okapi.Context, req *BillingAccountRef) error {
	return h.write(c, nil, func() (any, error) {
		a, err := h.svc.CancelAccount(c.Request().Context(), req.ExternalID)
		if err == nil {
			h.record(c, "billing.account.cancel", req.ExternalID, nil)
		}
		return a, err
	})
}

// PushSubscription applies, or with dry_run checks, a subscription push.
func (h *BillingHandler) PushSubscription(c *okapi.Context, req *BillingPushSubscriptionRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		res, err := h.svc.PushSubscription(c.Request().Context(), req.ExternalID, req.Body, req.DryRun)
		if err != nil {
			return nil, err
		}
		if res.DryRun != nil {
			return res.DryRun, nil
		}
		if res.Changed {
			h.record(c, "billing.subscription.push", req.ExternalID, map[string]any{"plan": req.Body.Plan, "status": req.Body.Status,
				"event_at": req.Body.EventAt, "limits": req.Body.Limits, "max_workspaces": req.Body.MaxWorkspaces})
		}
		return res.Account, nil
	})
}

// BillingSSOProviderRequest is PUT /sso-provider.
type BillingSSOProviderRequest struct {
	Body billing.SSOProviderInput `json:"body"`
}

// UpsertSSOProvider creates or updates the console-managed SSO provider (miabi-console).
func (h *BillingHandler) UpsertSSOProvider(c *okapi.Context, req *BillingSSOProviderRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		p, err := h.svc.UpsertSSOProvider(c.Request().Context(), req.Body)
		if err == nil {
			h.record(c, "billing.sso_provider.upsert", "", map[string]any{"issuer": p.Issuer, "adopted": p.Adopted, "group_sync": p.GroupSync})
		}
		return p, err
	})
}

// GetSSOProvider reports the console-managed SSO provider.
func (h *BillingHandler) GetSSOProvider(c *okapi.Context) error {
	p, err := h.svc.SSOProvider()
	if err != nil {
		return billingErr(c, err, "failed to read the SSO provider")
	}
	return ok(c, p)
}

// DeleteSSOProvider removes the console-managed SSO provider.
func (h *BillingHandler) DeleteSSOProvider(c *okapi.Context) error {
	return h.write(c, struct{}{}, func() (any, error) {
		if err := h.svc.DeleteSSOProvider(); err != nil {
			return nil, err
		}
		h.record(c, "billing.sso_provider.delete", "", nil)
		return map[string]string{"message": "SSO provider removed"}, nil
	})
}

// BillingDNSProviderRequest is PUT /accounts/{external_id}/workspaces/{workspace}/dns-provider.
type BillingDNSProviderRequest struct {
	ExternalID string                  `path:"external_id" required:"true"`
	Workspace  string                  `path:"workspace" required:"true" doc:"Workspace uid or handle"`
	Body       billing.DNSProviderPush `json:"body"`
}

// PushDNSProvider creates or updates the workspace's console-managed DNS provider.
func (h *BillingHandler) PushDNSProvider(c *okapi.Context, req *BillingDNSProviderRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		v, err := h.svc.PushDNSProvider(c.Request().Context(), req.ExternalID, req.Workspace, req.Body)
		if err == nil {
			h.record(c, "billing.dns_provider.upsert", req.ExternalID, map[string]any{"workspace": req.Workspace,
				"zones": v.Zones, "token_rotated": req.Body.APIToken != ""})
		}
		return v, err
	})
}

// GetDNSProvider reports the workspace's console-managed DNS provider.
func (h *BillingHandler) GetDNSProvider(c *okapi.Context, req *BillingWorkspaceRef) error {
	v, err := h.svc.DNSProvider(req.ExternalID, req.Workspace)
	if err != nil {
		return billingErr(c, err, "failed to read the DNS provider")
	}
	return ok(c, v)
}

// DeleteDNSProvider removes the workspace's console-managed DNS provider.
func (h *BillingHandler) DeleteDNSProvider(c *okapi.Context, req *BillingWorkspaceRef) error {
	return h.write(c, nil, func() (any, error) {
		if err := h.svc.DeleteDNSProvider(req.ExternalID, req.Workspace); err != nil {
			return nil, err
		}
		h.record(c, "billing.dns_provider.delete", req.ExternalID, map[string]any{"workspace": req.Workspace})
		return map[string]string{"message": "DNS provider removed"}, nil
	})
}

// BillingSuspendRequest is POST /accounts/{external_id}/suspend.
type BillingSuspendRequest struct {
	ExternalID string                 `path:"external_id" required:"true"`
	Body       billing.SuspendRequest `json:"body"`
}

// SuspendAccount places the billing platform's own hold on an account.
func (h *BillingHandler) SuspendAccount(c *okapi.Context, req *BillingSuspendRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		a, err := h.svc.SuspendAccount(c.Request().Context(), req.ExternalID, req.Body)
		if err == nil {
			h.record(c, "billing.account.suspend", req.ExternalID, map[string]any{"level": req.Body.Level, "reason": req.Body.Reason, "until": req.Body.Until})
		}
		return a, err
	})
}

// ResumeAccount lifts the billing platform's own hold.
func (h *BillingHandler) ResumeAccount(c *okapi.Context, req *BillingAccountRef) error {
	return h.write(c, nil, func() (any, error) {
		a, err := h.svc.ResumeAccount(c.Request().Context(), req.ExternalID)
		if err == nil {
			h.record(c, "billing.account.resume", req.ExternalID, nil)
		}
		return a, err
	})
}

// BillingAddWorkspaceRequest is POST /accounts/{external_id}/workspaces.
type BillingAddWorkspaceRequest struct {
	ExternalID string                  `path:"external_id" required:"true"`
	Body       billing.WorkspaceAttach `json:"body"`
}

// BillingWorkspaceRef names a workspace of an account by uid or handle.
type BillingWorkspaceRef struct {
	ExternalID string `path:"external_id" required:"true"`
	Workspace  string `path:"workspace" required:"true" doc:"Workspace uid or handle"`
}

// BillingRenameWorkspaceRequest is PATCH /accounts/{external_id}/workspaces/{workspace}.
type BillingRenameWorkspaceRequest struct {
	ExternalID string                  `path:"external_id" required:"true"`
	Workspace  string                  `path:"workspace" required:"true"`
	Body       billing.WorkspaceRename `json:"body"`
}

// BillingDeleteWorkspaceRequest is POST /accounts/{external_id}/workspaces/{workspace}/delete.
type BillingDeleteWorkspaceRequest struct {
	ExternalID string                  `path:"external_id" required:"true"`
	Workspace  string                  `path:"workspace" required:"true"`
	Body       billing.WorkspaceDelete `json:"body"`
}

// AddWorkspace creates a workspace for the account or attaches one its owner has.
func (h *BillingHandler) AddWorkspace(c *okapi.Context, req *BillingAddWorkspaceRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		a, err := h.svc.AddWorkspace(c.Request().Context(), req.ExternalID, req.Body)
		if err == nil {
			meta := map[string]any{"workspace": req.Body.Workspace}
			if req.Body.Create != nil {
				meta["create"] = req.Body.Create.Name
			}
			h.record(c, "billing.workspace.add", req.ExternalID, meta)
		}
		return a, err
	})
}

// DetachWorkspace stops billing a workspace without touching it otherwise.
func (h *BillingHandler) DetachWorkspace(c *okapi.Context, req *BillingWorkspaceRef) error {
	return h.write(c, nil, func() (any, error) {
		a, err := h.svc.DetachWorkspace(c.Request().Context(), req.ExternalID, req.Workspace)
		if err == nil {
			h.record(c, "billing.workspace.detach", req.ExternalID, map[string]any{"workspace": req.Workspace})
		}
		return a, err
	})
}

// RenameWorkspace changes a workspace's display name.
func (h *BillingHandler) RenameWorkspace(c *okapi.Context, req *BillingRenameWorkspaceRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		a, err := h.svc.RenameWorkspace(req.ExternalID, req.Workspace, req.Body)
		if err == nil {
			h.record(c, "billing.workspace.rename", req.ExternalID, map[string]any{"workspace": req.Workspace, "display_name": req.Body.DisplayName})
		}
		return a, err
	})
}

// DeleteWorkspace deletes a workspace of the account at once.
func (h *BillingHandler) DeleteWorkspace(c *okapi.Context, req *BillingDeleteWorkspaceRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		a, ws, err := h.svc.DeleteWorkspace(c.Request().Context(), req.ExternalID, req.Workspace, req.Body)
		if err == nil {
			h.record(c, "billing.workspace.delete", req.ExternalID, map[string]any{"workspace": ws.Name, "uid": ws.UID})
		}
		return a, err
	})
}

// Usage reports the account's workspaces' usage against their limits.
func (h *BillingHandler) Usage(c *okapi.Context, req *BillingAccountRef) error {
	u, err := h.svc.AccountUsage(req.ExternalID)
	if err != nil {
		return billingErr(c, err, "failed to read usage")
	}
	return ok(c, u)
}

func (h *BillingHandler) record(c *okapi.Context, action, externalID string, meta map[string]any) {
	actor := middlewares.UserID(c)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["api_key_id"] = middlewares.APIKeyID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: action, TargetType: "billing_account", TargetID: externalID, IP: c.RealIP(), Metadata: meta})
}

// idempotentReplayHeader marks an answer replayed from an earlier request with the same key.
const idempotentReplayHeader = "Idempotent-Replayed"

// write runs a Billing API write and answers it. With an Idempotency-Key the answer is stored, and
// a retry of the same request gets it back instead of applying the change twice. okapi offers no
// way to wrap the response writer, so writes build their envelope here rather than in a middleware.
func (h *BillingHandler) write(c *okapi.Context, body any, run func() (any, error)) error {
	key := strings.TrimSpace(c.Header("Idempotency-Key"))
	if key == "" {
		status, out := billingAnswer(run)
		return writeRaw(c, status, out)
	}
	if len(key) > 200 {
		return c.AbortWithError(http.StatusBadRequest, billing.NewCodedError(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "Idempotency-Key is longer than 200 characters"))
	}
	raw, _ := json.Marshal(body)
	r := c.Request()
	stored, err := h.svc.BeginIdempotent(key, billing.RequestHash(r.Method, r.URL.RequestURI(), raw))
	switch {
	case errors.Is(err, billing.ErrKeyReused):
		return c.AbortWithError(http.StatusUnprocessableEntity, billing.NewCodedError(http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED", err.Error()))
	case errors.Is(err, billing.ErrInFlight):
		return c.AbortWithError(http.StatusConflict, billing.NewCodedError(http.StatusConflict, "IDEMPOTENCY_KEY_IN_FLIGHT", err.Error()))
	case err != nil:
		return c.AbortInternalServerError("failed to check the Idempotency-Key", err)
	case stored != nil:
		c.Response().Header().Set(idempotentReplayHeader, "true")
		return writeRaw(c, stored.Status, stored.Body)
	}
	status, out := billingAnswer(run)
	if err := h.svc.FinishIdempotent(key, status, out); err != nil {
		logger.Error("failed to store a billing idempotent answer", "error", err)
	}
	return writeRaw(c, status, out)
}

// billingAnswer runs a write and renders its envelope; errors without a stable code become a 500
// without detail.
func billingAnswer(run func() (any, error)) (int, []byte) {
	data, err := run()
	if err == nil {
		out, merr := json.Marshal(dto.Response[any]{Success: true, Data: data})
		if merr == nil {
			return http.StatusOK, out
		}
		err = merr
	}
	status, info := http.StatusInternalServerError, dto.ErrorInfo{StatusCode: http.StatusInternalServerError,
		Code: "INTERNAL_SERVER_ERROR", Message: "the billing request failed"}
	var ce *billing.CodedError
	if errors.As(err, &ce) {
		status = ce.Status
		info = dto.ErrorInfo{StatusCode: ce.Status, Code: ce.Code(), Message: ce.Message, Error: ce.Message}
	} else {
		logger.Error("billing request failed", "error", err)
	}
	out, _ := json.Marshal(dto.ErrorResponseBody{Success: false, Error: &info})
	return status, out
}

func writeRaw(c *okapi.Context, status int, body []byte) error {
	w := c.Response()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err := w.Write(body)
	return err
}

// Billing API states reported to admins.
const (
	BillingAPIOff        = "off"
	BillingAPIUnlicensed = "unlicensed"
	BillingAPIOn         = "on"
)

// AdminInfo is platform information and statuses the admin screens share.
type AdminInfo struct {
	// BillingAPI is "off" (MIABI_BILLING_API_ENABLED unset), "unlicensed" (on, but the licence
	// lacks billing) or "on".
	BillingAPI string `json:"billing_api" enum:"off,unlicensed,on"`
}

// AdminInfoHandler serves GET /api/v1/admin/info.
type AdminInfoHandler struct {
	billingEnabled bool
	ee             enterprise.EE
}

// NewAdminInfoHandler builds the handler.
func NewAdminInfoHandler(billingEnabled bool, ee enterprise.EE) *AdminInfoHandler {
	return &AdminInfoHandler{billingEnabled: billingEnabled, ee: ee}
}

// Get reports the shared admin information.
func (h *AdminInfoHandler) Get(c *okapi.Context) error {
	return ok(c, AdminInfo{BillingAPI: BillingAPIState(h.billingEnabled, h.ee)})
}

// BillingAPIState combines the boot switch and the licence.
func BillingAPIState(enabled bool, ee enterprise.EE) string {
	switch {
	case !enabled:
		return BillingAPIOff
	case ee == nil || ee.Require(enterprise.FlagBilling) != nil:
		return BillingAPIUnlicensed
	}
	return BillingAPIOn
}
