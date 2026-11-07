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

// Package netguard checks the resolved IP at dial time so DNS rebinding cannot reach internal addresses.
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

// Call once at startup; unconfigured blocks private ranges.
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

// Hostnames are re-checked against their resolved IPs at dial time.
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

// Redirects are followed, but the dialer re-validates each hop.
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
