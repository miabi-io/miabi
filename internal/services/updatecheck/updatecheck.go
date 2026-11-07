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

// Package updatecheck checks versions.miabi.io daily and notifies; it never upgrades anything itself.
package updatecheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jkaninda/okapi/client"
	"github.com/miabi-io/miabi/internal/models"
	"golang.org/x/mod/semver"
	"gorm.io/gorm"
)

const (
	// The request carries nothing identifying beyond the User-Agent: no install id, host or license.
	IndexURL = "https://versions.miabi.io/v1/index.json"

	// httpTimeout bounds the whole exchange. A hung endpoint must never wedge cron.
	httpTimeout = 15 * time.Second
)

// Components checked, one cached row each. Named in models so the schema and the service agree.
const (
	ComponentMiabi   = models.UpdateComponentMiabi
	ComponentAgent   = models.UpdateComponentAgent
	ComponentRunner  = models.UpdateComponentRunner
	ComponentGateway = models.UpdateComponentGateway
)

// ReasonSecurity marks a release the admin notice must surface prominently.
const ReasonSecurity = "security"

// Release is one channel entry of versions.miabi.io.
type Release struct {
	Version      string    `json:"version"`
	ReleasedAt   time.Time `json:"released_at"`
	ReleaseURL   string    `json:"release_url"`
	ChangelogURL string    `json:"changelog_url,omitempty"`
	Reason       string    `json:"reason"`
}

// Channels is a component's current releases. Prerelease is offered only to a build that is itself one.
type Channels struct {
	Stable     *Release `json:"stable,omitempty"`
	Prerelease *Release `json:"prerelease,omitempty"`
}

// Index is the subset of versions.miabi.io/v1/index.json we rely on.
type Index struct {
	Components map[string]Channels `json:"components"`
}

// Service reads and writes one component's cached row.
type Service struct {
	db        *gorm.DB
	component string
	// Empty for a component this process doesn't run; the check then records the newest stable release.
	version string
	enabled bool
}

// NewService builds the cache for Miabi's own release.
func NewService(db *gorm.DB, version string, enabled bool) *Service {
	return &Service{db: db, component: ComponentMiabi, version: version, enabled: enabled}
}

// NewAgentService builds the cache for the node agent.
func NewAgentService(db *gorm.DB, enabled bool) *Service {
	return NewComponentService(db, ComponentAgent, enabled)
}

// NewComponentService builds the cache for a component this process does not run (agent, runner, gateway).
func NewComponentService(db *gorm.DB, component string, enabled bool) *Service {
	return &Service{db: db, component: component, enabled: enabled}
}

// Component names what this cache tracks.
func (s *Service) Component() string { return s.component }

// A dev build never checks its own component: its version doesn't compare meaningfully to releases.
func (s *Service) Enabled() bool {
	if s.version == "" {
		return s.enabled
	}
	return s.enabled && normalize(s.version) != ""
}

// Returns "" for non-versions such as "dev", "unknown" or a commit sha.
func normalize(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// A prerelease user wants newer betas and stable; a stable user must never be nudged onto a beta.
func wantPrerelease(current string) bool { return semver.Prerelease(current) != "" }

// The cached row lags upgrades until the next daily cron, so comparing at read time prevents offering a downgrade.
func IsNewer(current, latest string) bool {
	cur, lat := normalize(current), normalize(latest)
	if cur == "" || lat == "" {
		return false
	}
	return semver.Compare(lat, cur) > 0
}

// The channel is trusted only as far as the version agrees, so a prerelease never reaches a stable install.
func (s *Service) pick(ch Channels) *Release {
	if s.version == "" {
		if r := ch.Stable; r != nil && normalize(r.Version) != "" && semver.Prerelease(normalize(r.Version)) == "" {
			return r
		}
		return nil
	}
	cur := normalize(s.version)
	var best *Release
	for _, r := range []*Release{ch.Stable, ch.Prerelease} {
		v := normalize(r.versionOrEmpty())
		if v == "" || semver.Compare(v, cur) <= 0 || (semver.Prerelease(v) != "" && !wantPrerelease(cur)) {
			continue
		}
		if best == nil || semver.Compare(v, normalize(best.Version)) > 0 {
			best = r
		}
	}
	return best
}

func (r *Release) versionOrEmpty() string {
	if r == nil {
		return ""
	}
	return r.Version
}

// apply records a fresh index entry on the row.
func (s *Service) apply(st *models.UpdateStatus, ch Channels) {
	st.CheckedVersion = s.version
	r := s.pick(ch)
	if r == nil {
		st.LatestVersion, st.ReleaseURL, st.PublishedAt, st.Reason = "", "", nil, ""
		return
	}
	st.LatestVersion = normalize(r.Version)
	st.ReleaseURL = r.ReleaseURL
	if r.ChangelogURL != "" {
		st.ReleaseURL = r.ChangelogURL
	}
	published := r.ReleasedAt
	st.PublishedAt = &published
	st.Reason = r.Reason
}

// Status returns this component's cached row, creating it on first read.
func (s *Service) Status() (*models.UpdateStatus, error) {
	var st models.UpdateStatus
	err := s.db.Where("component = ?", s.component).First(&st).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = models.UpdateStatus{Component: s.component}
		if err := s.db.Create(&st).Error; err != nil {
			return nil, err
		}
		return &st, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// Recording the version rather than a boolean means the next release notifies again.
func (s *Service) Dismiss(version string) error {
	st, err := s.Status()
	if err != nil {
		return err
	}
	st.DismissedVersion = version
	return s.db.Save(st).Error
}

// Checker fetches the index once and updates every component's row from it.
type Checker struct {
	client   *client.Client
	url      string
	services []*Service
}

func NewChecker(version string, services ...*Service) *Checker {
	c := &Checker{services: services}
	c.setURL(IndexURL, version)
	return c
}

// setURL points the checker elsewhere. Only tests call it with anything but IndexURL.
func (c *Checker) setURL(url, version string) {
	c.url = url
	c.client = client.New(url,
		client.WithTimeout(httpTimeout),
		client.WithUserAgent("miabi/"+version),
		client.WithHeader("Accept", "application/json"),
	)
}

// Enabled reports whether any component is checked.
func (c *Checker) Enabled() bool {
	for _, s := range c.services {
		if s.Enabled() {
			return true
		}
	}
	return false
}

// Errors are stored on the rows, not returned: air-gapped installs fail daily and that must not be noisy.
func (c *Checker) Check(ctx context.Context) error {
	var rows []row
	for _, s := range c.services {
		if !s.Enabled() {
			continue
		}
		st, err := s.Status()
		if err != nil {
			return err
		}
		rows = append(rows, row{s, st})
	}
	if len(rows) == 0 {
		return nil
	}

	idx, notModified, etag, fetchErr := c.fetch(ctx, replayableETag(rows))

	now := time.Now()
	var errs []error
	for _, r := range rows {
		r.st.CheckedAt = &now
		switch {
		case fetchErr != nil:
			r.st.LastError = fetchErr.Error()
		case notModified:
			r.st.LastError = ""
		default:
			r.st.LastError, r.st.ETag = "", etag
			r.svc.apply(r.st, idx.Components[r.svc.component])
		}
		if err := r.svc.db.Save(r.st).Error; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// The ETag covers only the index, not the build, so replaying it after an upgrade would keep a stale verdict.
func replayableETag(rows []row) string {
	etag := rows[0].st.ETag
	for _, r := range rows {
		if r.st.ETag != etag || (r.svc.version != "" && r.st.CheckedVersion != r.svc.version) {
			return ""
		}
	}
	return etag
}

type row struct {
	svc *Service
	st  *models.UpdateStatus
}

// fetch GETs the index, replaying the stored ETag.
func (c *Checker) fetch(ctx context.Context, etag string) (idx Index, notModified bool, newETag string, err error) {
	req := c.client.Get("").WithContext(ctx)
	if etag != "" {
		req = req.Header("If-None-Match", etag)
	}
	resp, err := req.Do()
	if err != nil {
		return idx, false, "", err
	}
	switch resp.StatusCode {
	case http.StatusNotModified:
		return idx, true, resp.Header.Get("ETag"), nil
	case http.StatusOK:
	default:
		return idx, false, "", fmt.Errorf("%s returned %s", c.url, resp.Status)
	}
	if err := resp.JSON(&idx); err != nil {
		return idx, false, "", fmt.Errorf("decode %s: %w", c.url, err)
	}
	return idx, false, resp.Header.Get("ETag"), nil
}
