// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netguard provides SSRF-resistant HTTP clients for outbound requests to user-supplied
// URLs. It validates the *resolved* IP at dial time, so DNS rebinding cannot smuggle a request to
// an internal address. Loopback and link-local are always blocked; private ranges by default.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// allowPrivate toggles whether private (RFC1918/ULA) targets are permitted.
var allowPrivate atomic.Bool

// Configure sets whether private-range targets are allowed. Call once at
// startup. Default (unconfigured) blocks private ranges.
func Configure(allow bool) { allowPrivate.Store(allow) }

// ErrBlocked is returned when a destination IP is disallowed.
type ErrBlocked struct {
	IP   string
	Host string
}

func (e *ErrBlocked) Error() string {
	return fmt.Sprintf("destination %s (%s) is not an allowed webhook target", e.Host, e.IP)
}

func ipBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	if !allowPrivate.Load() && ip.IsPrivate() {
		return true
	}
	return false
}

// ValidateURL performs an up-front check: the scheme must be http(s), a host
// must be present, and any IP-literal host must be allowed. Hostnames are
// re-checked against their resolved IPs at dial time.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url has no host")
	}
	if ip := net.ParseIP(host); ip != nil && ipBlocked(ip) {
		return &ErrBlocked{IP: ip.String(), Host: host}
	}
	return nil
}

// safeDialContext resolves the host and dials only an allowed IP, rejecting the
// connection otherwise.
func safeDialContext(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ipAddr := range ips {
			if ipBlocked(ipAddr.IP) {
				lastErr = &ErrBlocked{IP: ipAddr.IP.String(), Host: host}
				continue
			}
			conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
			if derr != nil {
				lastErr = derr
				continue
			}
			return conn, nil
		}
		if lastErr == nil {
			lastErr = &ErrBlocked{Host: host}
		}
		return nil, lastErr
	}
}

// Client returns an HTTP client whose dialer enforces the SSRF policy. Redirects
// are followed but each hop is re-validated by the dialer.
func Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           safeDialContext(dialer),
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          10,
		},
	}
}
