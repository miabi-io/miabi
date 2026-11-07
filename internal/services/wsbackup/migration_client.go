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

package wsbackup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// MigrationSourceInfo is what a source install tells an importer about itself and what its key opens.
type MigrationSourceInfo struct {
	InstallID  string               `json:"install_id"`
	Version    string               `json:"version"`
	Schema     int                  `json:"schema"`
	Workspaces []MigrationWorkspace `json:"workspaces"`
	// CodeWorkspace is the workspace a pasted code was started for; empty for an instance migration.
	CodeWorkspace string `json:"code_workspace,omitempty"`
}

// MigrationWorkspace is one workspace a key may export.
type MigrationWorkspace struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Frozen      bool   `json:"frozen,omitempty"`
	// What moving it involves, for the importer's preview.
	Apps      int   `json:"apps"`
	Databases int   `json:"databases"`
	Volumes   int   `json:"volumes"`
	SizeBytes int64 `json:"size_bytes"`
}

// MigrationExportView is an export as the importer sees it.
type MigrationExportView struct {
	ID        uint                       `json:"id"`
	Workspace string                     `json:"workspace"`
	Status    models.BackupStatus        `json:"status"`
	Phase     string                     `json:"phase,omitempty"`
	Error     string                     `json:"error,omitempty"`
	Schema    int                        `json:"schema"`
	Frozen    bool                       `json:"frozen"`
	SizeBytes int64                      `json:"size_bytes"`
	Artifacts []models.MigrationArtifact `json:"artifacts"`
	Report    models.BundleReport        `json:"report"`
	ExpiresAt *time.Time                 `json:"expires_at,omitempty"`
}

// ExportView builds the importer's view of an export, leaving out where the source staged its files.
func ExportView(b *models.WorkspaceBundle) MigrationExportView {
	v := MigrationExportView{
		ID: b.ID, Workspace: b.SourceWorkspace, Status: b.Status, Phase: b.Phase, Error: b.Error,
		SizeBytes: b.SizeBytes, Report: b.Report, ExpiresAt: b.ExpiresAt, Artifacts: []models.MigrationArtifact{},
	}
	if m := b.Migration; m != nil {
		v.Schema, v.Frozen = m.Schema, m.Freeze
		for _, a := range m.Manifest {
			v.Artifacts = append(v.Artifacts, a.Public())
		}
	}
	return v
}

// ErrSourceUnreachable wraps a failure to talk to the source install at all.
var ErrSourceUnreachable = errors.New("the source Miabi could not be reached")

// migrationClient talks to a source install's migration API with a migration key.
type migrationClient struct {
	base string
	key  string
	http *http.Client
}

// caPEM, when given, is trusted in addition to the system roots, for a source behind a private CA.
func newMigrationClient(base, key string, insecure bool, caPEM string) (*migrationClient, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("the source URL must be an http(s) URL of the source Miabi, e.g. https://miabi.example.com")
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure} //nolint:gosec // only when the importer asks, and audited
	if strings.TrimSpace(caPEM) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("the CA bundle holds no PEM certificate")
		}
		tlsCfg.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	return &migrationClient{
		base: strings.TrimRight(u.String(), "/"),
		key:  strings.TrimSpace(key),
		http: &http.Client{Transport: tr},
	}, nil
}

func (c *migrationClient) url(path string) string { return c.base + "/api/v1/migration" + path }

func (c *migrationClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSourceUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		return remoteError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("the source answered something that is not a Miabi response: %w", err)
	}
	return json.Unmarshal(env.Data, out)
}

// remoteError turns a source's error envelope into an error that says what the source said.
func remoteError(status int, raw []byte) error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if status == http.StatusUnauthorized {
		return errors.New("the source refused the migration code: it was cancelled, it expired, or an import already completed with it. Start the migration again on the source")
	}
	if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
		return fmt.Errorf("the source refused (%d %s): %s", status, env.Error.Code, env.Error.Message)
	}
	if status == http.StatusNotFound {
		return errors.New("the source has no migration API: it is an older Miabi, or migration is not enabled there (MIABI_MIGRATION_ENABLED)")
	}
	return fmt.Errorf("the source answered %d", status)
}

func (c *migrationClient) info(ctx context.Context) (*MigrationSourceInfo, error) {
	var out MigrationSourceInfo
	return &out, c.do(ctx, http.MethodGet, "/workspaces", nil, &out)
}

func (c *migrationClient) createExport(ctx context.Context, ws string, freeze, data bool) (*MigrationExportView, error) {
	var out MigrationExportView
	body := map[string]bool{"freeze": freeze, "data": data}
	return &out, c.do(ctx, http.MethodPost, "/workspaces/"+url.PathEscape(ws)+"/exports", body, &out)
}

func (c *migrationClient) export(ctx context.Context, ws string, id uint) (*MigrationExportView, error) {
	var out MigrationExportView
	return &out, c.do(ctx, http.MethodGet, fmt.Sprintf("/workspaces/%s/exports/%d", url.PathEscape(ws), id), nil, &out)
}

func (c *migrationClient) state(ctx context.Context, ws string, id uint) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/workspaces/%s/exports/%d/state", url.PathEscape(ws), id)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSourceUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, remoteError(resp.StatusCode, raw)
	}
	return raw, nil
}

func (c *migrationClient) action(ctx context.Context, ws string, id uint, verb string, body any) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/workspaces/%s/exports/%d/%s", url.PathEscape(ws), id, verb), body, nil)
}

func (c *migrationClient) deleteExport(ctx context.Context, ws string, id uint) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/workspaces/%s/exports/%d", url.PathEscape(ws), id), nil, nil)
}

// downloadAttempts bounds the retries of one artifact; each retry resumes where the last one stopped.
const downloadAttempts = 5

func (c *migrationClient) download(ctx context.Context, ws string, id uint, n int, a models.MigrationArtifact, f *os.File) error {
	path := c.url(fmt.Sprintf("/workspaces/%s/exports/%d/artifacts/%d", url.PathEscape(ws), id, n))
	var lastErr error
	for attempt := 0; attempt < downloadAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		offset, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		if a.Size > 0 && offset >= a.Size {
			break
		}
		lastErr = c.fetchFrom(ctx, path, offset, f)
		if lastErr == nil {
			break
		}
		if errors.Is(lastErr, errNoResume) {
			if err := f.Truncate(0); err != nil {
				return err
			}
		}
	}
	if lastErr != nil {
		return fmt.Errorf("download %s: %w", a.File, lastErr)
	}
	return verifyFile(f, a)
}

var errNoResume = errors.New("the source restarted the download")

func (c *migrationClient) fetchFrom(ctx context.Context, path string, offset int64, f *os.File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSourceUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode == http.StatusOK && offset > 0:
		// The source sent the whole file again: start over rather than append a second copy.
		return errNoResume
	case resp.StatusCode != http.StatusOK:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return remoteError(resp.StatusCode, raw)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	return err
}

func verifyFile(f *os.File, a models.MigrationArtifact) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if a.Size > 0 && n != a.Size {
		return fmt.Errorf("%s arrived with %d bytes, the source recorded %d", a.File, n, a.Size)
	}
	if a.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), a.SHA256) {
		return fmt.Errorf("%s failed its checksum: it changed or was corrupted in transit", a.File)
	}
	_, err = f.Seek(0, io.SeekStart)
	return err
}
