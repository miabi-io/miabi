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

package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jkaninda/okapi/client"
	"github.com/miabi-io/miabi/internal/config"
)

const (
	exportPath = "/v1/export"
	// Caps the body so a hostile endpoint can't exhaust memory.
	maxBundleBytes = 32 << 20 // 32 MiB
)

var errBundleTooLarge = errors.New("marketplace export: response exceeds size limit")

// The base URL is operator-configured, so it is trusted.
type Client struct {
	url  string // the resolved bundle URL (static export.json, or a server's /v1/export)
	http *client.Client
}

func NewClient(base string) *Client {
	httpClient := &http.Client{
		Transport: &capTransport{base: http.DefaultTransport, max: maxBundleBytes},
	}
	return &Client{
		url: resolveExportURL(base),
		http: client.New("",
			client.WithHTTPClient(httpClient),
			client.WithTimeout(30*time.Second),
			client.WithUserAgent("miabi/"+config.Version),
		),
	}
}

// Lets MIABI_MARKETPLACE_URL point at a static CDN export.json or a self-hosted marketplace service.
func resolveExportURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(strings.ToLower(base), ".json") {
		return base
	}
	return base + exportPath
}

func (c *Client) Fetch(ctx context.Context, etag string) (data []byte, newETag string, notModified bool, err error) {
	rb := c.http.Get(c.url).WithContext(ctx)
	if etag != "" {
		rb = rb.Header("If-None-Match", etag)
	}
	resp, err := rb.Do()
	if err != nil {
		return nil, "", false, fmt.Errorf("fetch marketplace bundle: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, etag, true, nil
	case http.StatusOK:
		return resp.Body, resp.Header.Get("ETag"), false, nil
	default:
		return nil, "", false, fmt.Errorf("marketplace export: unexpected status %d", resp.StatusCode)
	}
}

type capTransport struct {
	base http.RoundTripper
	max  int64
}

func (t *capTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &capReader{rc: resp.Body, remaining: t.max}
	return resp, nil
}

// Fails closed rather than truncating, which would surface as a confusing decode error.
type capReader struct {
	rc        io.ReadCloser
	remaining int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.remaining < 0 {
		return 0, errBundleTooLarge
	}
	n, err := c.rc.Read(p)
	c.remaining -= int64(n)
	if c.remaining < 0 {
		return n, errBundleTooLarge
	}
	return n, err
}

func (c *capReader) Close() error { return c.rc.Close() }
