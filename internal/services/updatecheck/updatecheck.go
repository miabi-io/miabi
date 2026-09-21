// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package updatecheck asks GitHub, once a day, whether a newer release of a component exists and caches the
// answer. It notifies; it never upgrades. This process holds the Docker socket and orchestrates every
// workspace, so restarting itself into a new image is a decision for a human with a shell — and the node
// agent carries the only remote path to its host, so the same is true there, more so.
//
// Two components are checked, one row each: the platform itself, and the node agent. They differ in one way
// that shapes the code. The platform knows the build it is running, so a check can answer "is there anything
// newer than me". Every node runs its own agent build, so there is no single current version to compare
// against: the check records the newest release and the comparison happens per node, at read time.
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
	// githubAPI is the base URL; a component's path lists its releases newest-first. Deliberately NOT
	// /releases/latest: that endpoint excludes prereleases, and a project whose releases are all prereleases
	// gets a 404 from it — the check would silently never fire.
	githubAPI = "https://api.github.com"

	// httpTimeout bounds the whole exchange. A hung GitHub must never wedge cron.
	httpTimeout = 15 * time.Second
)

// Components checked, one cached row each. Named in models so the schema and the service agree.
const (
	ComponentMiabi = models.UpdateComponentMiabi
	ComponentAgent = models.UpdateComponentAgent
)

// Repositories releases are read from.
const (
	RepoMiabi = "miabi-io/miabi"
	RepoAgent = "miabi-io/agent"
)

// Release is the subset of GitHub's release object we rely on.
type Release struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

// Service performs the check for one component and persists its result.
type Service struct {
	db        *gorm.DB
	client    *client.Client
	component string
	repo      string
	// version is the running build, e.g. "1.0.0-beta.4" or "dev". Empty for a component this process does
	// not itself run, where the check records the newest release rather than a verdict about "me".
	version    string
	enabled    bool
	stableOnly bool
}

// Options describes one component's check.
type Options struct {
	Component string
	Repo      string // "owner/name"
	// Version is the running build. Leave empty for a component that runs elsewhere.
	Version string
	Enabled bool
	// StableOnly drops every prerelease, whatever the running build is. The platform leaves this false
	// so a beta install still hears about the next beta; the agent sets it, because an operator is not
	// choosing a channel per node and must never be nudged onto a release candidate by a badge.
	StableOnly bool
}

// New builds a checker for one component.
func New(db *gorm.DB, opts Options) *Service {
	s := &Service{
		db: db, component: opts.Component, repo: opts.Repo,
		version: opts.Version, enabled: opts.Enabled, stableOnly: opts.StableOnly,
	}
	s.setBaseURL(githubAPI)
	return s
}

// NewService builds the checker for Miabi's own release.
func NewService(db *gorm.DB, version string, enabled bool) *Service {
	return New(db, Options{Component: ComponentMiabi, Repo: RepoMiabi, Version: version, Enabled: enabled})
}

// NewAgentService builds the checker for the node agent. It tracks no local build — every node reports its
// own — and it is stable-only.
func NewAgentService(db *gorm.DB, enabled bool) *Service {
	return New(db, Options{Component: ComponentAgent, Repo: RepoAgent, Enabled: enabled, StableOnly: true})
}

// Component names what this checker tracks.
func (s *Service) Component() string { return s.component }

// releasesPath is the component's release list.
func (s *Service) releasesPath() string { return "/repos/" + s.repo + "/releases" }

// setBaseURL rebuilds the HTTP client against another origin. Only tests call it (pointing at an httptest
// server); production always talks to api.github.com. The User-Agent identifies the client honestly and is
// the ONLY thing this request says about the install: no id, no host, no license.
func (s *Service) setBaseURL(base string) {
	s.client = client.New(base,
		client.WithTimeout(httpTimeout),
		client.WithUserAgent("miabi/"+s.version),
		client.WithHeader("Accept", "application/vnd.github+json"),
	)
}

// Enabled reports whether checks run at all. A `dev` build never checks its own component: its version does
// not compare meaningfully against any release. A component this process does not run carries no version and
// is gated on the setting alone.
func (s *Service) Enabled() bool {
	if s.version == "" {
		return s.enabled
	}
	return s.enabled && normalize(s.version) != ""
}

// normalize turns a baked version into a semver string x/mod accepts. The binary is stamped without the
// leading "v" (ldflags pass the Docker tag, e.g. "1.0.0-beta.4"), while GitHub tags carry it. Returns ""
// when the value is not a version at all — "dev", "unknown", or a commit sha.
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

// wantPrerelease reports whether the running build is itself a prerelease. A
// user on v1.0.0-beta.4 wants to hear about beta.5 AND about stable v1.0.0; a
// user on stable must never be nudged onto a beta.
func wantPrerelease(current string) bool { return semver.Prerelease(current) != "" }

// isStable reports whether a release is a final one. Both halves matter: GitHub's own prerelease flag is a
// checkbox a maintainer can forget, and the agent carries a "v0.1-rc.1" tag that was never ticked. Reading
// the tag as well means a release candidate cannot reach a node because of a missed checkbox.
func isStable(r Release, tag string) bool {
	return !r.Prerelease && semver.Prerelease(tag) == ""
}

// pickNewest returns the highest release, skipping drafts, anything at or below min (when given), and every
// prerelease when stableOnly. Ordering is semver, never lexical: "v1.0.0-beta.10" sorts after
// "v1.0.0-beta.9", and "v1.0.0" after both — a string compare gets both backwards, which would tell a
// beta.10 user to "upgrade" to beta.9.
func pickNewest(releases []Release, stableOnly bool, min string) (Release, bool) {
	var best Release
	var bestTag string
	for _, r := range releases {
		if r.Draft {
			continue
		}
		tag := normalize(r.TagName)
		if tag == "" {
			continue
		}
		if stableOnly && !isStable(r, tag) {
			continue
		}
		if min != "" && semver.Compare(tag, min) <= 0 {
			continue
		}
		if bestTag == "" || semver.Compare(tag, bestTag) > 0 {
			best, bestTag = r, tag
		}
	}
	return best, bestTag != ""
}

// Newest picks the newest release the running build should be offered, or false when none is newer.
func Newest(current string, releases []Release) (Release, bool) {
	cur := normalize(current)
	if cur == "" {
		return Release{}, false
	}
	return pickNewest(releases, !wantPrerelease(cur), cur)
}

// Latest picks the newest release outright, for a component this process does not run. There is no current
// version to measure against — every node has its own — so the comparison is left to the reader, per node.
func Latest(releases []Release, stableOnly bool) (Release, bool) {
	return pickNewest(releases, stableOnly, "")
}

// IsNewer reports whether latest is a strictly newer release than current. Readers gate the notice on this
// rather than trusting the cached row: the row is written by a daily cron, so between an upgrade and the next
// tick it still describes the previous build. Comparing at read time makes offering a downgrade impossible.
func IsNewer(current, latest string) bool {
	cur, lat := normalize(current), normalize(latest)
	if cur == "" || lat == "" {
		return false
	}
	return semver.Compare(lat, cur) > 0
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

// Dismiss silences the notice for a specific version. Recording the version —
// rather than a boolean — means the next release notifies again.
func (s *Service) Dismiss(version string) error {
	st, err := s.Status()
	if err != nil {
		return err
	}
	st.DismissedVersion = version
	return s.db.Save(st).Error
}

// Check fetches the release list and updates the cached row. Errors are stored
// on the row rather than returned to a user: an air-gapped install fails this
// every day, and that must be visible to an admin without being noisy.
func (s *Service) Check(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	st, err := s.Status()
	if err != nil {
		return err
	}
	now := time.Now()
	st.CheckedAt = &now

	// The cached verdict is a function of two inputs: GitHub's release list AND the version compared against it.
	// The ETag covers only the first, so replaying it after an upgrade would earn a 304 and preserve a verdict
	// that no longer applies. Drop the ETag, forcing this check to re-evaluate the list against the new build.
	// A component with no local build has only the one input, so its ETag is always safe to replay.
	etagToSend := st.ETag
	if s.version != "" && st.CheckedVersion != s.version {
		etagToSend = ""
	}

	releases, notModified, etag, err := s.fetch(ctx, etagToSend)
	if err != nil {
		st.LastError = err.Error()
		return s.db.Save(st).Error
	}
	st.LastError = ""
	if etag != "" {
		st.ETag = etag
	}
	if notModified {
		// The list is unchanged AND the build is unchanged (or we would not have sent
		// the ETag), so the cached verdict still holds.
		return s.db.Save(st).Error
	}

	st.CheckedVersion = s.version
	if r, ok := s.pick(releases); ok {
		st.LatestVersion = normalize(r.TagName)
		st.ReleaseURL = r.HTMLURL
		published := r.PublishedAt
		st.PublishedAt = &published
	} else {
		st.LatestVersion, st.ReleaseURL, st.PublishedAt = "", "", nil
	}
	return s.db.Save(st).Error
}

// pick selects the release to cache: the newest above the running build when this process runs the
// component, else the newest outright.
func (s *Service) pick(releases []Release) (Release, bool) {
	if s.version != "" {
		return Newest(s.version, releases)
	}
	return Latest(releases, s.stableOnly)
}

// fetch GETs the release list, replaying the stored ETag. A 304 means the list
// is unchanged and, importantly, costs no rate-limit quota — the unauthenticated
// budget is 60 requests/hour per IP.
func (s *Service) fetch(ctx context.Context, etag string) (rel []Release, notModified bool, newETag string, err error) {
	req := s.client.Get(s.releasesPath()).
		WithContext(ctx).
		QueryParam("per_page", "20")
	if etag != "" {
		req = req.Header("If-None-Match", etag)
	}

	resp, err := req.Do()
	if err != nil {
		return nil, false, "", err
	}

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, true, resp.Header.Get("ETag"), nil
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, false, "", fmt.Errorf("github rate limit reached; will retry on the next scheduled check")
	default:
		return nil, false, "", fmt.Errorf("github returned %s", resp.Status)
	}

	if err := resp.JSON(&rel); err != nil {
		return nil, false, "", fmt.Errorf("decode releases: %w", err)
	}
	return rel, false, resp.Header.Get("ETag"), nil
}
