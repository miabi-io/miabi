// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mailer sends Miabi's own platform notification emails — password resets, workspace invitations,
// account welcomes — over a system SMTP server. Sends are best-effort and asynchronous: with SMTP
// unconfigured every method is a silent no-op, and a nil *Service is safe too, so callers never guard.
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
)

// Service renders and sends platform notification emails.
type Service struct {
	smtp      config.SystemSMTPConfig
	appName   string
	appURL    string
	templates map[string]*template.Template
}

// NewService builds a mailer from the system SMTP config, the product name, and
// the public app URL (used to build action links in emails).
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
	for _, name := range []string{TemplatePasswordReset, TemplateWorkspaceInvitation, TemplateWelcome} {
		s.templates[name] = template.Must(template.ParseFS(templateFS, "templates/base.tmpl", "templates/"+name+".tmpl"))
	}
	return s
}

// IsConfigured reports whether a system SMTP server is set (host + from). Safe on
// a nil receiver.
func (s *Service) IsConfigured() bool {
	return s != nil && s.smtp.IsConfigured()
}

// SendPasswordReset emails a password-reset link. expiryHours is shown to the
// user; token is appended to the reset URL.
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

// dispatch renders and sends in the background, logging any failure. A no-op when
// SMTP is not configured (or the service is nil).
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

// link builds an absolute URL on the app's public base. An optional key/value
// pair is appended as a query string. When the app URL is unset the path is
// returned as-is (a relative link is better than a broken absolute one).
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
