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

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/billing"
)

// billingRoutes registers the Billing API. The group is disabled (404, left out of the OpenAPI
// document) unless MIABI_BILLING_API_ENABLED is set at boot; even then each call needs a billing
// key and a licence with the billing flag.
func (r *Router) billingRoutes() []okapi.RouteDefinition {
	g := r.v1.Group("/billing").WithTagInfo(okapi.GroupTag{Name: "Billing", Description: "The Billing API for an external billing platform (Enterprise, off by default)."})
	if !r.cfg.BillingAPIEnabled {
		g.Disable()
	}
	billingKey := []okapi.Middleware{r.authenticate, middlewares.RequireBillingCaller(), middlewares.RequireLicensed(r.ee, enterprise.FlagBilling)}
	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/plans",
			Group:       g,
			Middlewares: billingKey,
			Handler:     r.h.billing.Plans,
			Summary:     "List the plans a billing platform can map its packages to",
			Response:    &dto.Response[[]handlers.BillingPlan]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/plans/schema",
			Group:       g,
			Middlewares: billingKey,
			Handler:     r.h.billing.PlanSchema,
			Summary:     "List the plan fields this Miabi accepts from a console",
			Response:    &dto.Response[billing.PlanSchema]{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/plans/{key}",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.PushPlan),
			Summary:     "Create or update a console-managed plan",
			Description: "Adopts an unmanaged plan of the same name. Instance-local fields (placement, storage classes, runners) are never changed. Unknown fields come back in ignored. With dry_run=true, data is a PlanDryRun.",
			Request:     &handlers.BillingPushPlanRequest{},
			Response:    &dto.Response[billing.PlanPushResult]{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/plans/{key}",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.RemovePlan),
			Summary:     "Withdraw a console-managed plan",
			Description: "Deactivated while workspaces use it (or it is the default plan), deleted otherwise.",
			Request:     &handlers.BillingPlanRef{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/sso-provider",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.UpsertSSOProvider),
			Summary:     "Create or update the console's SSO provider (miabi-console)",
			Description: "Adopts a hand-configured provider with the same issuer. The issuer must be on the connected console's host. Send an Idempotency-Key.",
			Request:     &handlers.BillingSSOProviderRequest{},
			Response:    &dto.Response[billing.SSOProvider]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/sso-provider",
			Group:       g,
			Middlewares: billingKey,
			Handler:     r.h.billing.GetSSOProvider,
			Summary:     "Get the console's SSO provider",
			Response:    &dto.Response[billing.SSOProvider]{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/sso-provider",
			Group:       g,
			Middlewares: billingKey,
			Handler:     r.h.billing.DeleteSSOProvider,
			Summary:     "Remove the console's SSO provider",
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/accounts/{external_id}",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.UpsertAccount),
			Summary:     "Create or update a billing account",
			Description: "Matches the owner by e-mail (creating a missing one) and optionally creates the first workspace. Send an Idempotency-Key.",
			Request:     &handlers.BillingUpsertAccountRequest{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/accounts/{external_id}",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.GetAccount),
			Summary:     "Get a billing account",
			Request:     &handlers.BillingAccountRef{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/accounts/{external_id}",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.CancelAccount),
			Summary:     "Cancel a billing account",
			Description: "The account's workspaces are kept for the operator, under a hard restriction.",
			Request:     &handlers.BillingAccountRef{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/accounts/{external_id}/subscription",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.PushSubscription),
			Summary:     "Push the account's subscription state",
			Description: "Ordered by event_at: an older push answers 409 BILLING_STALE_EVENT. With dry_run=true, reports the limits each workspace would break instead (data is a DryRun).",
			Request:     &handlers.BillingPushSubscriptionRequest{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/accounts/{external_id}/suspend",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.SuspendAccount),
			Summary:     "Put a hold on a billing account",
			Description: "soft makes the account's workspaces read-only, hard suspends them, until resumed or until passes. The subscription's own level still applies when higher.",
			Request:     &handlers.BillingSuspendRequest{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/accounts/{external_id}/resume",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.ResumeAccount),
			Summary:     "Lift the hold on a billing account",
			Request:     &handlers.BillingAccountRef{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/accounts/{external_id}/workspaces",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.AddWorkspace),
			Summary:     "Create a workspace for the account, or attach one its owner has",
			Description: "Refused past max_workspaces (409 BILLING_WORKSPACE_LIMIT). The workspace takes the account's plan, add-ons and restriction.",
			Request:     &handlers.BillingAddWorkspaceRequest{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/accounts/{external_id}/workspaces/{workspace}",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.DetachWorkspace),
			Summary:     "Detach a workspace from the account",
			Description: "Restores the plan it had before it was attached and removes billing's add-ons and restriction. The last workspace of an active account cannot be detached (409 BILLING_LAST_WORKSPACE).",
			Request:     &handlers.BillingWorkspaceRef{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodPatch,
			Path:        "/accounts/{external_id}/workspaces/{workspace}",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.RenameWorkspace),
			Summary:     "Rename a workspace (display name only)",
			Request:     &handlers.BillingRenameWorkspaceRequest{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/accounts/{external_id}/workspaces/{workspace}/dns-provider",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.PushDNSProvider),
			Summary:     "Create or update the workspace's console DNS provider (miabi-console)",
			Description: "The API URL is the connected console's. Domains under the zones are connected and verified. api_token is required on the first push. Send an Idempotency-Key.",
			Request:     &handlers.BillingDNSProviderRequest{},
			Response:    &dto.Response[billing.DNSProviderView]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/accounts/{external_id}/workspaces/{workspace}/dns-provider",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.GetDNSProvider),
			Summary:     "Get the workspace's console DNS provider",
			Request:     &handlers.BillingWorkspaceRef{},
			Response:    &dto.Response[billing.DNSProviderView]{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/accounts/{external_id}/workspaces/{workspace}/dns-provider",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.DeleteDNSProvider),
			Summary:     "Remove the workspace's console DNS provider",
			Description: "Domains connected to it fall back to manual DNS.",
			Request:     &handlers.BillingWorkspaceRef{},
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/accounts/{external_id}/workspaces/{workspace}/delete",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.DeleteWorkspace),
			Summary:     "Delete a workspace now",
			Description: "Immediate and irreversible: confirm must repeat the workspace handle. The last workspace of an active account cannot be deleted (409 BILLING_LAST_WORKSPACE).",
			Request:     &handlers.BillingDeleteWorkspaceRequest{},
			Response:    &dto.Response[billing.Account]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/accounts/{external_id}/usage",
			Group:       g,
			Middlewares: billingKey,
			Handler:     okapi.H(r.h.billing.Usage),
			Summary:     "Usage of the account's workspaces against their limits",
			Request:     &handlers.BillingAccountRef{},
			Response:    &dto.Response[billing.Usage]{},
		},
	}
}
