// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailer

import (
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/config"
)

func configured() config.SystemSMTPConfig {
	return config.SystemSMTPConfig{Host: "smtp.example.com", Port: 587, From: "Miabi <noreply@example.com>", Encryption: "starttls"}
}

func TestIsConfigured(t *testing.T) {
	if (*Service)(nil).IsConfigured() {
		t.Error("nil service must not be configured")
	}
	if NewService(config.SystemSMTPConfig{}, "Miabi", "").IsConfigured() {
		t.Error("empty SMTP must not be configured")
	}
	if !NewService(configured(), "Miabi", "https://miabi.example.com").IsConfigured() {
		t.Error("host+from must be configured")
	}
}

func TestRenderTemplates(t *testing.T) {
	s := NewService(configured(), "Miabi", "https://miabi.example.com/")

	reset, err := s.render(TemplatePasswordReset, map[string]any{
		"AppName": "Miabi", "AppURL": s.appURL, "Subject": "Reset your password",
		"UserName": "jane@example.com", "ResetURL": s.link("/reset-password", "token", "abc123"), "ExpiryHours": 1,
	})
	if err != nil {
		t.Fatalf("render reset: %v", err)
	}
	for _, want := range []string{"Reset your password", "https://miabi.example.com/reset-password?token=abc123", "Miabi", "1 hour"} {
		if !strings.Contains(reset, want) {
			t.Errorf("password reset email missing %q", want)
		}
	}

	inv, err := s.render(TemplateWorkspaceInvitation, map[string]any{
		"AppName": "Miabi", "AppURL": s.appURL, "Subject": "x",
		"WorkspaceName": "Acme", "InviterName": "Jonas", "Role": "admin", "RoleArticle": article("admin"),
		"AcceptURL": s.link("/invitations/accept", "token", "t0k"), "ExpiresAt": time.Now().Format("Jan 2, 2006"),
	})
	if err != nil {
		t.Fatalf("render invitation: %v", err)
	}
	for _, want := range []string{"Acme", "Jonas", "an <strong>admin</strong>", "/invitations/accept?token=t0k"} {
		if !strings.Contains(inv, want) {
			t.Errorf("invitation email missing %q", want)
		}
	}
}

func TestArticle(t *testing.T) {
	for word, want := range map[string]string{"admin": "an", "editor": "an", "owner": "an", "viewer": "a", "developer": "a", "": "a"} {
		if got := article(word); got != want {
			t.Errorf("article(%q) = %q, want %q", word, got, want)
		}
	}
}

func TestNilServiceDispatchIsSafe(t *testing.T) {
	// Must not panic on a nil service or an unconfigured one.
	(*Service)(nil).SendWelcome("a@b.com", "A")
	NewService(config.SystemSMTPConfig{}, "Miabi", "").SendPasswordReset("a@b.com", "A", "tok", 1)
}

func TestLink(t *testing.T) {
	s := NewService(configured(), "Miabi", "https://miabi.example.com/")
	if got := s.link("/login"); got != "https://miabi.example.com/login" {
		t.Errorf("link(/login) = %q", got)
	}
	if got := s.link("/reset-password", "token", "a b"); got != "https://miabi.example.com/reset-password?token=a+b" {
		t.Errorf("link with query = %q", got)
	}
	if got := s.link("/reset-password", "token", ""); got != "https://miabi.example.com/reset-password" {
		t.Errorf("empty token must omit query: %q", got)
	}
}

// A template that fails to parse or references a missing field only breaks at
// send time, in a goroutine, where nobody sees it. Rendering every one here is
// the cheapest way to keep that from shipping.
func TestRenderEveryTemplate(t *testing.T) {
	s := NewService(configured(), "Miabi", "https://miabi.example.com/")

	t.Run("welcome", func(t *testing.T) {
		out, err := s.render(TemplateWelcome, map[string]any{
			"AppName": "Miabi", "AppURL": s.appURL, "Subject": "Welcome",
			"UserName": "Jane", "LoginURL": s.link("/login"),
		})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		for _, want := range []string{"Welcome to Miabi", "Jane", "https://miabi.example.com/login"} {
			if !strings.Contains(out, want) {
				t.Errorf("welcome email missing %q", want)
			}
		}
	})

	t.Run("email verification", func(t *testing.T) {
		out, err := s.render(TemplateEmailVerification, map[string]any{
			"AppName": "Miabi", "AppURL": s.appURL, "Subject": "Verify your email address",
			"UserName": "Jane", "VerifyURL": s.link("/verify-email", "token", "v3r1fy"), "ExpiryHours": 48,
		})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		for _, want := range []string{
			"Verify your email address", "Jane",
			"https://miabi.example.com/verify-email?token=v3r1fy", "48 hours",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("verification email missing %q", want)
			}
		}
	})

	t.Run("workspace role change", func(t *testing.T) {
		out, err := s.render(TemplateWorkspaceRoleChange, map[string]any{
			"AppName": "Miabi", "AppURL": s.appURL, "Subject": "Your role changed",
			"UserName": "Jane", "WorkspaceName": "Acme", "ActorName": "Jonas",
			"OldRole": "developer", "NewRole": "viewer", "RoleSummary": roleSummary("viewer"),
			"WorkspaceURL": s.link("/"),
		})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		// Both roles must appear: "your role changed" without saying from what is
		// not much of a notice.
		for _, want := range []string{"Acme", "Jonas", "developer", "viewer", "read-only"} {
			if !strings.Contains(out, want) {
				t.Errorf("role change email missing %q", want)
			}
		}
	})
}

func TestRoleSummary(t *testing.T) {
	for _, role := range []string{"owner", "admin", "developer", "viewer"} {
		if got := roleSummary(role); got == "" || !strings.Contains(strings.ToLower(got), role) {
			t.Errorf("roleSummary(%q) = %q, want it to name the role", role, got)
		}
	}
	// An unknown role still gets a sentence rather than an empty paragraph.
	if roleSummary("sorcerer") == "" {
		t.Error("an unknown role produced no summary")
	}
}
