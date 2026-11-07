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

package domain

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const dnsQueryTimeout = 5 * time.Second

func authoritativeLookupTXT(ctx context.Context, host string) ([]string, error) {
	if txts, err := authoritativeTXT(ctx, host); err == nil {
		return txts, nil
	}
	// Still live DNS, only subject to caching lag, so the ownership guarantee holds.
	return net.DefaultResolver.LookupTXT(ctx, host)
}

// Queries authoritative nameservers directly to bypass recursive-resolver caching.
func authoritativeTXT(ctx context.Context, host string) ([]string, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	nameservers, err := authoritativeNS(ctx, host)
	if err != nil {
		return nil, err
	}
	client := &dns.Client{Timeout: dnsQueryTimeout}
	lastErr := errors.New("no authoritative nameserver answered")
	for _, ns := range nameservers {
		addrs, aerr := net.DefaultResolver.LookupHost(ctx, strings.TrimSuffix(ns, "."))
		if aerr != nil || len(addrs) == 0 {
			lastErr = aerr
			continue
		}
		msg := new(dns.Msg)
		msg.SetQuestion(dns.Fqdn(host), dns.TypeTXT)
		resp, _, qerr := client.ExchangeContext(ctx, msg, net.JoinHostPort(addrs[0], "53"))
		if qerr != nil {
			lastErr = qerr
			continue
		}
		out := make([]string, 0, len(resp.Answer))
		for _, ans := range resp.Answer {
			if t, ok := ans.(*dns.TXT); ok {
				// A long TXT value is split at 255 bytes on the wire.
				out = append(out, strings.Join(t.Txt, ""))
			}
		}
		return out, nil
	}
	return nil, lastErr
}

// NS delegation is stable and safe to resolve recursively, unlike the freshly-created TXT.
func authoritativeNS(ctx context.Context, host string) ([]string, error) {
	labels := strings.Split(host, ".")
	lastErr := errors.New("no delegation found")
	for i := 0; i+1 < len(labels); i++ { // stop before the bare TLD
		zone := strings.Join(labels[i:], ".")
		nss, err := net.DefaultResolver.LookupNS(ctx, zone)
		if err == nil && len(nss) > 0 {
			out := make([]string, len(nss))
			for j, n := range nss {
				out[j] = n.Host
			}
			return out, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
