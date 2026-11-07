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

// Package mailer sends platform emails best-effort and async; unconfigured SMTP or a nil Service is a silent no-op.
package mailer

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"embed"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/config"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// Template names (each pairs with templates/<name>.tmpl rendered inside base.tmpl).
const (
	TemplatePasswordReset       = "password_reset"
	TemplateWorkspaceInvitation = "workspace_invitation"
	TemplateWelcome             = "welcome"
	TemplateEmailVerification   = "email_verification"
	TemplateWorkspaceRoleChange = "workspace_role_change"
)

// Service renders and sends platform notification emails.
type Service struct {
	smtp      config.SystemSMTPConfig
	appName   string
	appURL    string
	templates map[string]*template.Template
}

func NewService(smtp config.SystemSMTPConfig, appName, appURL string) *Service {
	if strings.TrimSpace(appName) == "" {
		appName = "Miabi"
	}
	s := &Service{
		smtp:      smtp,
		appName:   appName,
		appURL:    strings.TrimRight(strings.TrimSpace(appURL), "/"),
		templates: map[string]*template.Template{},
	}
	// A missing name fails only at send time inside a goroutine; mailer_test renders all of them.
	for _, name := range []string{
		TemplatePasswordReset, TemplateWorkspaceInvitation, TemplateWelcome,
		TemplateEmailVerification, TemplateWorkspaceRoleChange,
	} {
		s.templates[name] = template.Must(template.ParseFS(templateFS, "templates/base.tmpl", "templates/"+name+".tmpl"))
	}
	return s
}

func (s *Service) IsConfigured() bool {
	return s != nil && s.smtp.IsConfigured()
}

func (s *Service) SendPasswordReset(to, name, token string, expiryHours int) {
	if !s.IsConfigured() {
		return
	}
	s.dispatch(to, "Reset your password", TemplatePasswordReset, map[string]any{
		"UserName":    nameOr(name, to),
		"ResetURL":    s.link("/reset-password", "token", token),
		"ExpiryHours": expiryHours,
	})
}

// SendWorkspaceInvitation emails a workspace invitation with an accept link.
func (s *Service) SendWorkspaceInvitation(to, workspaceName, inviterName, role, token string, expiresAt time.Time) {
	if !s.IsConfigured() {
		return
	}
	s.dispatch(to, "You've been invited to a workspace", TemplateWorkspaceInvitation, map[string]any{
		"WorkspaceName": workspaceName,
		"InviterName":   nameOr(inviterName, "A teammate"),
		"Role":          role,
		"RoleArticle":   article(role),
		"AcceptURL":     s.link("/invitations/accept", "token", token),
		"ExpiresAt":     expiresAt.Format("Jan 2, 2006"),
	})
}

// SendWelcome emails a new account holder a welcome with a sign-in link.
func (s *Service) SendWelcome(to, name string) {
	if !s.IsConfigured() {
		return
	}
	s.dispatch(to, "Welcome to "+s.appName, TemplateWelcome, map[string]any{
		"UserName": nameOr(name, to),
		"LoginURL": s.link("/login"),
	})
}

func (s *Service) SendEmailVerification(to, name, token string, expiryHours int) {
	if !s.IsConfigured() {
		return
	}
	s.dispatch(to, "Verify your email address", TemplateEmailVerification, map[string]any{
		"UserName":    nameOr(name, to),
		"VerifyURL":   s.link("/verify-email", "token", token),
		"ExpiryHours": expiryHours,
	})
}

func (s *Service) SendWorkspaceRoleChanged(to, name, workspaceName, actorName, oldRole, newRole string) {
	if !s.IsConfigured() {
		return
	}
	s.dispatch(to, "Your role in "+workspaceName+" changed", TemplateWorkspaceRoleChange, map[string]any{
		"UserName":      nameOr(name, to),
		"WorkspaceName": workspaceName,
		"ActorName":     nameOr(actorName, "A workspace owner"),
		"OldRole":       oldRole,
		"NewRole":       newRole,
		"RoleSummary":   roleSummary(newRole),
		"WorkspaceURL":  s.link("/"),
	})
}

func roleSummary(role string) string {
	switch strings.ToLower(role) {
	case "owner":
		return "As an owner you have full control of the workspace, including billing, members and deletion."
	case "admin":
		return "As an admin you can manage the workspace's resources and its members."
	case "developer":
		return "As a developer you can deploy and manage applications, databases and domains."
	case "viewer":
		return "As a viewer you have read-only access: you can see the workspace but not change it."
	default:
		return "Your permissions in the workspace have changed."
	}
}

func (s *Service) dispatch(to, subject, tmpl string, data map[string]any) {
	if !s.IsConfigured() {
		logger.Debug("mailer: system SMTP not configured, skipping", "template", tmpl, "to", to)
		return
	}
	go func() {
		if err := s.send(to, subject, tmpl, data); err != nil {
			logger.Error("mailer: failed to send notification", "template", tmpl, "to", to, "error", err)
		}
	}()
}

func (s *Service) send(to, subject, tmpl string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	data["AppName"] = s.appName
	data["AppURL"] = s.appURL
	data["Subject"] = subject
	html, err := s.render(tmpl, data)
	if err != nil {
		return err
	}
	return sendMail(s.smtp, []string{to}, subject, html)
}

func (s *Service) render(name string, data map[string]any) (string, error) {
	t, ok := s.templates[name]
	if !ok {
		return "", fmt.Errorf("mailer: unknown template %q", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// With no app URL the path is returned as-is: a relative link beats a broken absolute one.
func (s *Service) link(path string, kv ...string) string {
	u := s.appURL + path
	if len(kv) >= 2 && kv[1] != "" {
		u += "?" + url.QueryEscape(kv[0]) + "=" + url.QueryEscape(kv[1])
	}
	return u
}

func nameOr(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}

func article(word string) string {
	w := strings.ToLower(strings.TrimSpace(word))
	if w == "" {
		return "a"
	}
	switch w[0] {
	case 'a', 'e', 'i', 'o', 'u':
		return "an"
	}
	return "a"
}
