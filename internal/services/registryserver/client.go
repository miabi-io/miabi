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

package registryserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Accept every schema (docker v2 / OCI, single or index) so a tag resolves regardless.
var manifestAccept = strings.Join([]string{
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
}, ", ")

// Client reaches the internal registry with per-request Basic auth, since the credential needs crypto.Init first.
type Client struct {
	base string
	http *http.Client
}

// A nil authHeader or empty result sends no Authorization.
func NewClient(baseURL string, authHeader func() string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 30 * time.Second,
			// A transport, not per-call headers: one forgotten call site would fail only at run time.
			Transport: &basicAuthTransport{header: authHeader},
		},
	}
}

// basicAuthTransport stamps the registry credential on every outgoing request.
type basicAuthTransport struct {
	header func() string
	base   http.RoundTripper
}

func (t *basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt := t.base
	if rt == nil {
		rt = http.DefaultTransport
	}
	if t.header != nil {
		if h := t.header(); h != "" {
			// Clone: RoundTrip must not modify the request it is given.
			req = req.Clone(req.Context())
			req.Header.Set("Authorization", h)
		}
	}
	return rt.RoundTrip(req)
}

func (c *Client) Catalog(ctx context.Context) ([]string, error) {
	var repos []string
	next := "/v2/_catalog?n=200"
	for next != "" {
		var page struct {
			Repositories []string `json:"repositories"`
		}
		link, err := c.getJSON(ctx, next, &page)
		if err != nil {
			return nil, err
		}
		repos = append(repos, page.Repositories...)
		next = nextLink(link)
	}
	return repos, nil
}

// Tags returns the tags of a repository (empty when the repo has none).
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error) {
	var out struct {
		Tags []string `json:"tags"`
	}
	if _, err := c.getJSON(ctx, "/v2/"+escapePath(repo)+"/tags/list", &out); err != nil {
		return nil, err
	}
	return out.Tags, nil
}

// ManifestDigest resolves a tag to its content digest (needed to delete it).
func (c *Client) ManifestDigest(ctx context.Context, repo, ref string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v2/"+escapePath(repo)+"/manifests/"+url.PathEscape(ref), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", manifestAccept)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry: manifest %s/%s: %s", repo, ref, resp.Status)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("registry: no digest for %s/%s", repo, ref)
	}
	return digest, nil
}

// TagManifest re-PUTs the digest's manifest bytes under the tag, without re-uploading blobs.
func (c *Client) TagManifest(ctx context.Context, repo, digest, tag string) error {
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v2/"+escapePath(repo)+"/manifests/"+url.PathEscape(digest), nil)
	if err != nil {
		return err
	}
	get.Header.Set("Accept", manifestAccept)
	resp, err := c.http.Do(get)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry: get manifest %s@%s: %s", repo, digest, resp.Status)
	}
	ct := resp.Header.Get("Content-Type")

	put, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+"/v2/"+escapePath(repo)+"/manifests/"+url.PathEscape(tag), bytes.NewReader(body))
	if err != nil {
		return err
	}
	put.Header.Set("Content-Type", ct)
	pResp, err := c.http.Do(put)
	if err != nil {
		return err
	}
	defer func() { _ = pResp.Body.Close() }()
	_, _ = io.Copy(io.Discard, pResp.Body)
	if pResp.StatusCode != http.StatusCreated && pResp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry: tag manifest %s:%s: %s", repo, tag, pResp.Status)
	}
	return nil
}

type manifest struct {
	Config struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"layers"`
	// Manifests is present on a manifest list / OCI index (multi-arch).
	Manifests []struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"manifests"`
}

// BlobSizes returns per-blob sizes so namespace usage is summed with cross-tag dedup.
func (c *Client) BlobSizes(ctx context.Context, repo, ref string) (map[string]int64, error) {
	_, sizes, err := c.manifestBlobs(ctx, repo, ref)
	return sizes, err
}

// One manifest fetch yields both values, halving round trips per tag.
func (c *Client) ManifestInfo(ctx context.Context, repo, ref string) (digest string, sizeBytes int64, err error) {
	digest, sizes, err := c.manifestBlobs(ctx, repo, ref)
	if err != nil {
		return "", 0, err
	}
	for _, sz := range sizes {
		sizeBytes += sz
	}
	return digest, sizeBytes, nil
}

// Recurses into index children so a multi-arch image counts each shared layer once.
func (c *Client) manifestBlobs(ctx context.Context, repo, ref string) (string, map[string]int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v2/"+escapePath(repo)+"/manifests/"+url.PathEscape(ref), nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("registry: manifest %s/%s: %s", repo, ref, resp.Status)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	var m manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return "", nil, fmt.Errorf("registry: decode manifest %s/%s: %w", repo, ref, err)
	}
	sizes := make(map[string]int64)
	if m.Config.Digest != "" {
		sizes[m.Config.Digest] = m.Config.Size
	}
	for _, l := range m.Layers {
		sizes[l.Digest] = l.Size
	}
	// Index: recurse into each child manifest (deduped by digest across arches).
	for _, sub := range m.Manifests {
		child, err := c.BlobSizes(ctx, repo, sub.Digest)
		if err != nil {
			continue // best-effort sizing
		}
		for d, s := range child {
			sizes[d] = s
		}
	}
	return digest, sizes, nil
}

// The registry has no delete-by-tag (needs REGISTRY_STORAGE_DELETE_ENABLED); deleting untags every tag on it.
func (c *Client) DeleteManifest(ctx context.Context, repo, digest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/v2/"+escapePath(repo)+"/manifests/"+url.PathEscape(digest), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusMethodNotAllowed:
		return ErrDeleteDisabled
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("registry: delete %s@%s: %s", repo, digest, resp.Status)
	}
}

// A 404 is reported as ErrNotFound.
func (c *Client) getJSON(ctx context.Context, path string, out any) (link string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("registry: GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "", fmt.Errorf("registry: decode %s: %w", path, err)
	}
	return resp.Header.Get("Link"), nil
}

func nextLink(link string) string {
	if !strings.Contains(link, `rel="next"`) {
		return ""
	}
	start := strings.IndexByte(link, '<')
	end := strings.IndexByte(link, '>')
	if start < 0 || end <= start {
		return ""
	}
	return link[start+1 : end]
}
