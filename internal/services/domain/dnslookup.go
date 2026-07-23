// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// dnsQueryTimeout bounds a single authoritative query.
const dnsQueryTimeout = 5 * time.Second

func authoritativeLookupTXT(ctx context.Context, host string) ([]string, error) {
	if txts, err := authoritativeTXT(ctx, host); err == nil {
		return txts, nil
	}
	// Fall back to the recursive/system resolver. Still live DNS — just subject to
	// caching/propagation lag — so the ownership guarantee holds.
	return net.DefaultResolver.LookupTXT(ctx, host)
}

// authoritativeTXT finds the nameservers responsible for host and queries them
// directly for its TXT records, bypassing recursive-resolver caching.
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
				// A TXT rdata is a set of character-strings; join them (a single long
				// value is split at 255 bytes on the wire).
				out = append(out, strings.Join(t.Txt, ""))
			}
		}
		return out, nil
	}
	return nil, lastErr
}

// authoritativeNS returns the nameservers responsible for host, looking up NS for
// host and walking up parent labels until a delegation answers. NS delegation is
// stable and safe to resolve recursively (unlike the freshly-created TXT).
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
