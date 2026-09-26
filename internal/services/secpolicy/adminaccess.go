// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package secpolicy

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// KindAdminAccess governs the admin console unlock. Platform scope only.
const KindAdminAccess = "admin_access"

// FactorTOTP is the one unlock factor built today; passkey, PIN and SSO step-up are not.
const FactorTOTP = "totp"

// AdminAccessSpec is the admin console unlock policy.
type AdminAccessSpec struct {
	V               int      `json:"v"`
	Required        bool     `json:"required"`
	Factors         []string `json:"factors,omitempty"`
	TTL             string   `json:"ttl,omitempty"`
	IdleTimeout     string   `json:"idle_timeout,omitempty"`
	SensitiveWindow string   `json:"sensitive_window,omitempty"`
	MaxAttempts     int      `json:"max_attempts,omitempty"`
	Lockout         string   `json:"lockout,omitempty"`
	// AllowedIPs restricts the unlock and the admin API (API keys included) to these addresses or
	// CIDRs. It trusts the client IP the platform resolves, so it is only as good as the proxy setup.
	AllowedIPs []string `json:"allowed_ips,omitempty"`
}

// AdminAccess is a parsed spec with its durations resolved.
type AdminAccess struct {
	Required        bool
	TTL             time.Duration
	IdleTimeout     time.Duration
	SensitiveWindow time.Duration
	MaxAttempts     int
	Lockout         time.Duration
	AllowedIPs      []netip.Prefix
}

// DefaultAdminAccess is the unlock Community gets when MIABI_ADMIN_UNLOCK is on.
func DefaultAdminAccess() AdminAccess {
	return AdminAccess{Required: true, TTL: 30 * time.Minute, IdleTimeout: 10 * time.Minute,
		SensitiveWindow: 5 * time.Minute, MaxAttempts: 5, Lockout: 15 * time.Minute}
}

// ParseAdminAccessSpec decodes and validates an admin_access spec, filling defaults.
func ParseAdminAccessSpec(raw string) (AdminAccessSpec, AdminAccess, error) {
	var s AdminAccessSpec
	out := DefaultAdminAccess()
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, out, fmt.Errorf("%w: %v", ErrInvalidSpec, err)
	}
	if s.V == 0 {
		s.V = 1
	}
	if s.V != 1 {
		return s, out, fmt.Errorf("%w: unsupported admin_access spec version %d", ErrInvalidSpec, s.V)
	}
	if len(s.Factors) == 0 {
		s.Factors = []string{FactorTOTP}
	}
	for _, f := range s.Factors {
		if f != FactorTOTP {
			return s, out, fmt.Errorf("%w: factor %q is not available yet; only %q is", ErrInvalidSpec, f, FactorTOTP)
		}
	}
	durations := []struct {
		name string
		raw  string
		dst  *time.Duration
	}{{"ttl", s.TTL, &out.TTL}, {"idle_timeout", s.IdleTimeout, &out.IdleTimeout},
		{"sensitive_window", s.SensitiveWindow, &out.SensitiveWindow}, {"lockout", s.Lockout, &out.Lockout}}
	for _, d := range durations {
		if d.raw == "" {
			continue
		}
		v, err := time.ParseDuration(d.raw)
		if err != nil || v < time.Minute || v > 24*time.Hour {
			return s, out, fmt.Errorf("%w: %s must be a duration between 1m and 24h", ErrInvalidSpec, d.name)
		}
		*d.dst = v
	}
	if out.IdleTimeout > out.TTL {
		return s, out, fmt.Errorf("%w: idle_timeout cannot exceed ttl", ErrInvalidSpec)
	}
	if s.MaxAttempts < 0 || s.MaxAttempts > 50 {
		return s, out, fmt.Errorf("%w: max_attempts must be between 1 and 50", ErrInvalidSpec)
	}
	if s.MaxAttempts > 0 {
		out.MaxAttempts = s.MaxAttempts
	}
	for _, ip := range s.AllowedIPs {
		p, err := parsePrefix(ip)
		if err != nil {
			return s, out, fmt.Errorf("%w: allowed_ips entry %q is not an IP or CIDR", ErrInvalidSpec, ip)
		}
		out.AllowedIPs = append(out.AllowedIPs, p)
	}
	out.Required = s.Required
	return s, out, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// AdminAccessPolicy returns the enforced admin_access rule, or ok=false when none is in force.
// Only the platform scope counts: the console is one per platform.
func (s *Service) AdminAccessPolicy() (AdminAccess, bool) {
	if !s.Active() {
		return AdminAccess{}, false
	}
	rows, err := s.rows(KindAdminAccess)
	if err != nil {
		return AdminAccess{}, false
	}
	for i := range rows {
		if rows[i].ScopeType != ScopePlatform || rows[i].Mode != ModeEnforce {
			continue
		}
		_, a, perr := ParseAdminAccessSpec(rows[i].Spec)
		if perr != nil {
			return AdminAccess{}, false
		}
		return a, true
	}
	return AdminAccess{}, false
}
