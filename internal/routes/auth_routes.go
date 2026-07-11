// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/handlers"
)

// authRoutes registers public auth endpoints plus the authenticated profile/logout.
func (r *Router) authRoutes() []okapi.RouteDefinition {
	auth := r.v1.Group("/auth").WithTagInfo(okapi.GroupTag{Name: "Auth", Description: "Registration, login, sessions, and password reset."})
	protected := []okapi.Middleware{r.authenticate}
	// Throttle unauthenticated, abuse-prone endpoints (per IP + path).
	limited := []okapi.Middleware{r.authRateLimit}

	return []okapi.RouteDefinition{
		{
			Method:   http.MethodGet,
			Path:     "/status",
			Group:    auth,
			Handler:  r.h.auth.Status,
			Summary:  "Auth feature status",
			Response: &dto.Response[handlers.AuthStatus]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/login",
			Group:       auth,
			Middlewares: limited,
			Handler:     okapi.H(r.h.auth.Login),
			Summary:     "Log in",
			Request:     &handlers.LoginRequest{},
			Response:    &dto.Response[handlers.AuthResponse]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/forgot-password",
			Group:       auth,
			Middlewares: limited,
			Handler:     okapi.H(r.h.auth.ForgotPassword),
			Summary:     "Request a password reset",
			Request:     &handlers.ForgotPasswordRequest{},
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/reset-password",
			Group:       auth,
			Middlewares: limited,
			Handler:     okapi.H(r.h.auth.ResetPassword),
			Summary:     "Reset password with a token",
			Request:     &handlers.ResetPasswordRequest{},
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/complete-password-reset",
			Group:       auth,
			Middlewares: limited,
			Handler:     okapi.H(r.h.auth.CompletePasswordReset),
			Summary:     "Finish a forced password change (exchanges a reset-session token for a session)",
			Request:     &handlers.CompletePasswordResetRequest{},
			Response:    &dto.Response[handlers.AuthResponse]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/change-password",
			Group:       auth,
			Middlewares: protected,
			Handler:     okapi.H(r.h.auth.ChangePassword),
			Summary:     "Change password (authenticated)",
			Request:     &handlers.ChangePasswordRequest{},
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/logout",
			Group:       auth,
			Middlewares: protected,
			Handler:     r.h.auth.Logout,
			Summary:     "Log out (revoke session)",
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/2fa/setup",
			Group:       auth,
			Middlewares: protected,
			Handler:     r.h.auth.Setup2FA,
			Summary:     "Begin two-factor (TOTP) setup",
			Response:    &dto.Response[handlers.Setup2FAResponse]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/2fa/verify",
			Group:       auth,
			Middlewares: protected,
			Handler:     okapi.H(r.h.auth.Verify2FA),
			Summary:     "Confirm and enable two-factor",
			Request:     &handlers.Verify2FARequest{},
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/2fa/disable",
			Group:       auth,
			Middlewares: protected,
			Handler:     okapi.H(r.h.auth.Disable2FA),
			Summary:     "Disable two-factor",
			Request:     &handlers.Disable2FARequest{},
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/2fa/recovery-codes",
			Group:       auth,
			Middlewares: protected,
			Handler:     okapi.H(r.h.auth.RegenerateRecoveryCodes),
			Summary:     "Regenerate recovery codes",
			Request:     &handlers.RegenerateCodesRequest{},
			Response:    &dto.Response[handlers.RecoveryCodesResponse]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/me",
			Group:       r.v1,
			Middlewares: protected,
			Handler:     r.h.auth.Me,
			Tags:        []string{"Auth"},
			Summary:     "Current user profile",
			Response:    &dto.Response[handlers.UserProfile]{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/me",
			Group:       r.v1,
			Middlewares: protected,
			Handler:     okapi.H(r.h.auth.UpdateProfile),
			Tags:        []string{"Auth"},
			Summary:     "Update current user profile",
			Request:     &handlers.UpdateProfileRequest{},
			Response:    &dto.Response[handlers.UserProfile]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/me/onboarding/dismiss",
			Group:       r.v1,
			Middlewares: protected,
			Handler:     r.h.auth.DismissOnboarding,
			Tags:        []string{"Auth"},
			Summary:     "Dismiss the getting-started checklist",
			Response:    &dto.Response[handlers.UserProfile]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/me/sessions",
			Group:       r.v1,
			Middlewares: protected,
			Handler:     r.h.auth.ListSessions,
			Tags:        []string{"Auth"},
			Summary:     "List active sessions",
			Response:    &dto.Response[[]handlers.SessionInfo]{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/me/sessions/{id}",
			Group:       r.v1,
			Middlewares: protected,
			Handler:     r.h.auth.RevokeSession,
			Tags:        []string{"Auth"},
			Summary:     "Revoke a session",
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/me/sessions/revoke-others",
			Group:       r.v1,
			Middlewares: protected,
			Handler:     r.h.auth.RevokeOtherSessions,
			Tags:        []string{"Auth"},
			Summary:     "Revoke all other sessions",
			Response:    &dto.Response[handlers.RevokeOthersResult]{},
		},
	}
}

// apiKeyRoutes registers API key management (JWT/session authenticated).
func (r *Router) apiKeyRoutes() []okapi.RouteDefinition {
	keys := r.v1.Group("/api-keys").WithTagInfo(okapi.GroupTag{Name: "API Keys", Description: "Long-lived programmatic access tokens."})
	protected := []okapi.Middleware{r.authenticate}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodPost,
			Path:        "",
			Group:       keys,
			Middlewares: protected,
			Handler:     okapi.H(r.h.apiKey.Create),
			Summary:     "Create an API key",
			Request:     &handlers.CreateAPIKeyRequest{},
			Response:    &dto.Response[handlers.APIKeyCreated]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "",
			Group:       keys,
			Middlewares: protected,
			Handler:     r.h.apiKey.List,
			Summary:     "List API keys",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{id}",
			Group:       keys,
			Middlewares: protected,
			Handler:     r.h.apiKey.Get,
			Summary:     "Get an API key",
		},
		{
			Method:      http.MethodPost,
			Path:        "/{id}/revoke",
			Group:       keys,
			Middlewares: protected,
			Handler:     r.h.apiKey.Revoke,
			Summary:     "Revoke an API key",
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{id}",
			Group:       keys,
			Middlewares: protected,
			Handler:     r.h.apiKey.Delete,
			Summary:     "Delete a revoked or expired API key",
			Response:    &dto.Response[dto.MessageData]{},
		},
	}
}
