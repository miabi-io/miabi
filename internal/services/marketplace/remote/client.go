// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
)

const (
	exportPath = "/v1/export"
	// maxBundleBytes caps the response body so a misbehaving or hostile endpoint
	// can't exhaust memory. The real catalog is a few hundred KiB.
	maxBundleBytes = 32 << 20 // 32 MiB
)

var errBundleTooLarge = errors.New("marketplace export: response exceeds size limit")

// Client fetches the marketplace export bundle over the Okapi HTTP client. The
// base URL is operator-configured (MIABI_MARKETPLACE_URL), so it is trusted; the
// transport negotiates and transparently decompresses gzip.
type Client struct {
	url  string // the resolved bundle URL (static export.json, or a server's /v1/export)
	http *client.Client
}

// NewClient builds a client for the marketplace at base. base may be either a
// static bundle URL (a CDN export.json, used directly) or a server base URL (the
// /v1/export path is appended) — see resolveExportURL.
func NewClient(base string) *Client {
	httpClient := &http.Client{
		Transport: &capTransport{base: http.DefaultTransport, max: maxBundleBytes},
	}
	return &Client{
		url: resolveExportURL(base),
		http: client.New("",
			client.WithHTTPClient(httpClient),
			client.WithTimeout(30*time.Second),
			client.WithUserAgent("miabi-marketplace-sync"),
		),
	}
}

// resolveExportURL picks the bundle URL: a direct .json (e.g. the jsDelivr export.json) is used as-is;
// anything else is treated as a server base and gets the /v1/export path appended. This lets
// MIABI_MARKETPLACE_URL point at either static git/CDN or a self-hosted marketplace service.
func resolveExportURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(strings.ToLower(base), ".json") {
		return base
	}
	return base + exportPath
}

// Fetch does a conditional GET of the bundle. When etag is non-empty it is sent
// as If-None-Match; a 304 yields notModified=true with no body (the cached
// bundle is still current). A 200 returns the body and the new ETag.
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

// capTransport wraps a RoundTripper to bound each response body, so the Okapi
// client's full-body read can never consume more than max bytes.
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

// capReader fails closed once more than the configured budget has been read,
// rather than silently truncating (which would surface as a confusing decode
// error downstream).
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
