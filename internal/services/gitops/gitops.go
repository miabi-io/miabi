// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gitops is the declarative, pull-based half of Miabi's GitOps &
// CI/CD model. A GitSource binds a Git repo of miabi.io/v1 manifests to a
// workspace; the reconciler clones at a ref, renders + diffs against live
// state, and converges by reusing the apply engine (Git → desired → existing
// reconciler → Docker). It adds no container plumbing of its own.
package gitops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/apply"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound     = errors.New("git source not found")
	ErrNameTaken    = errors.New("a git source with this name already exists")
	ErrNameRequired = errors.New("name is required")
	ErrURLRequired  = errors.New("select a git repository (or provide a repository URL)")
	ErrRepoNotFound = errors.New("git repository not found")
)

// Applier is the subset of the apply engine the reconciler drives, kept as an
// interface so the package stays testable.
type Applier interface {
	Apply(ctx context.Context, workspaceID uint, manifests []byte, opts apply.Options) (*apply.Result, error)
	ApplyResource(ctx context.Context, workspaceID uint, manifests []byte, opts apply.Options, kind, name string) (*apply.Result, error)
	DeleteResource(ctx context.Context, workspaceID uint, kind, name string) (*apply.Result, error)
	Teardown(ctx context.Context, workspaceID uint, sourceID string) (*apply.Result, error)
	Plan(ctx context.Context, workspaceID uint, manifests []byte, opts apply.Options) (*declarative.Plan, *declarative.ResourceSet, error)
	Topology(ctx context.Context, workspaceID uint, manifests []byte, opts apply.Options) (*apply.Topology, error)
	LiveTopology(ctx context.Context, workspaceID uint, syncError string) (*apply.Topology, error)
	LiveStatus(workspaceID uint) map[string]string
}

// Service manages GitSource bindings and reconciles them.
type Service struct {
	repo     *repositories.GitSourceRepository
	gitRepos *repositories.GitRepoRepository
	applier  Applier
}

// NewService wires the GitOps service.
func NewService(repo *repositories.GitSourceRepository, gitRepos *repositories.GitRepoRepository, applier Applier) *Service {
	return &Service{repo: repo, gitRepos: gitRepos, applier: applier}
}

// Input is the create/update payload for a GitSource. Name is the desired unique
// slug handle; DisplayName is the free-text label (falls back to Name when blank).
type Input struct {
	Name            string
	DisplayName     string
	RepoURL         string
	Ref             string
	Path            string
	GitRepositoryID *uint
	SyncPolicy      models.GitSyncPolicy
	Prune           bool
	SelfHeal        bool
	AllowEmpty      bool
}

// Create registers a new GitSource and assigns it a webhook secret.
func (s *Service) Create(workspaceID uint, in Input) (*models.GitSource, error) {
	in.normalize()
	name := slug.Make(in.Name, "")
	if name == "" {
		return nil, ErrNameRequired
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(in.Name)
	}
	// A selected git repository supplies both the URL and the clone credentials.
	if in.GitRepositoryID != nil {
		gr, err := s.gitRepos.FindInWorkspace(workspaceID, *in.GitRepositoryID)
		if err != nil {
			return nil, ErrRepoNotFound
		}
		in.RepoURL = gr.URL
	}
	if in.RepoURL == "" {
		return nil, ErrURLRequired
	}
	if taken, _ := s.repo.ExistsByName(workspaceID, name); taken {
		return nil, ErrNameTaken
	}
	src := &models.GitSource{
		WorkspaceID: workspaceID, Name: name, DisplayName: displayName, RepoURL: in.RepoURL,
		Ref: in.Ref, Path: in.Path, GitRepositoryID: in.GitRepositoryID,
		SyncPolicy: in.SyncPolicy, Prune: in.Prune, SelfHeal: in.SelfHeal, AllowEmpty: in.AllowEmpty,
		WebhookSecret: declarative.RandAlphaNum(40), Status: models.GitSourceUnknown,
	}
	if err := s.repo.Create(src); err != nil {
		return nil, err
	}
	return src, nil
}

// Update mutates an existing GitSource's configuration.
func (s *Service) Update(workspaceID, id uint, in Input) (*models.GitSource, error) {
	src, err := s.get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	in.normalize()
	if in.Name != "" && in.Name != src.Name {
		if taken, _ := s.repo.ExistsByName(workspaceID, in.Name); taken {
			return nil, ErrNameTaken
		}
		src.Name = in.Name
	}
	// A selected git repository supplies the URL + credentials; otherwise keep an
	// explicitly provided URL.
	if in.GitRepositoryID != nil {
		gr, err := s.gitRepos.FindInWorkspace(workspaceID, *in.GitRepositoryID)
		if err != nil {
			return nil, ErrRepoNotFound
		}
		src.RepoURL = gr.URL
	} else if in.RepoURL != "" {
		src.RepoURL = in.RepoURL
	}
	if in.Ref != "" {
		src.Ref = in.Ref
	}
	if in.Path != "" {
		src.Path = in.Path
	}
	src.GitRepositoryID = in.GitRepositoryID
	src.SyncPolicy = in.SyncPolicy
	src.Prune = in.Prune
	src.SelfHeal = in.SelfHeal
	src.AllowEmpty = in.AllowEmpty
	if err := s.repo.Update(src); err != nil {
		return nil, err
	}
	return src, nil
}

func (s *Service) Get(workspaceID, id uint) (*models.GitSource, error) { return s.get(workspaceID, id) }

func (s *Service) List(workspaceID uint) ([]models.GitSource, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// Delete removes a GitOps project. When cascade is set, the resources the project
// created (those carrying its gitops-source label) are torn down first, in
// dependency-safe order, before the source row is removed. Without cascade the
// resources are left running (orphaned from GitOps management).
// Returns the teardown result (nil when cascade is off) so callers can show which
// resources were removed and any that failed.
func (s *Service) Delete(ctx context.Context, workspaceID, id uint, cascade bool) (*apply.Result, error) {
	src, err := s.get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	var res *apply.Result
	if cascade {
		res, err = s.applier.Teardown(ctx, workspaceID, sourceLabel(src))
		if err != nil {
			return nil, fmt.Errorf("tear down resources: %w", err)
		}
	}
	if err := s.repo.Delete(workspaceID, id); err != nil {
		return nil, err
	}
	return res, nil
}

// sourceLabel is the value stored on every resource a source creates
// (miabi.io/gitops-source), used to scope listing and teardown to one project.
func sourceLabel(src *models.GitSource) string {
	return strconv.FormatUint(uint64(src.ID), 10)
}

func (s *Service) get(workspaceID, id uint) (*models.GitSource, error) {
	src, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return src, nil
}

// Diff clones the source and returns the desired-vs-live plan without applying
// — the data behind the GitOps screen's diff viewer.
func (s *Service) Diff(ctx context.Context, workspaceID, id uint) (*declarative.Plan, error) {
	src, err := s.get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	manifests, _, err := s.fetch(ctx, src)
	if err != nil {
		return nil, err
	}
	plan, _, err := s.applier.Plan(ctx, workspaceID, manifests, apply.Options{Prune: src.Prune, OwnerSource: sourceLabel(src)})
	return plan, err
}

// Topology clones the source and returns the resource graph (nodes + dependency
// edges) behind the project-detail topology view.
func (s *Service) Topology(ctx context.Context, workspaceID, id uint) (*apply.Topology, error) {
	src, err := s.get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	manifests, _, err := s.fetch(ctx, src)
	if err != nil {
		// Clone/parse of the current ref failed (e.g. a broken commit pushed after
		// a good sync). Degrade to the live state so the view still shows what is
		// deployed plus the failure, instead of returning an error to the UI.
		return s.applier.LiveTopology(ctx, workspaceID, fmt.Sprintf("git sync failed: %v", err))
	}
	return s.applier.Topology(ctx, workspaceID, manifests, apply.Options{Prune: src.Prune, OwnerSource: sourceLabel(src)})
}

// Status returns the live runtime status of managed resources, keyed by topology
// node key. Cheap (no clone) so the detail page can poll it.
func (s *Service) Status(workspaceID, id uint) (map[string]string, error) {
	if _, err := s.get(workspaceID, id); err != nil {
		return nil, err
	}
	return s.applier.LiveStatus(workspaceID), nil
}

// Sync reconciles a source addressed by workspace+id (manual trigger / webhook).
func (s *Service) Sync(ctx context.Context, workspaceID, id uint) (*models.GitSource, error) {
	src, err := s.get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	return src, s.Reconcile(ctx, src)
}

// SyncResource reconciles a single resource (kind/name) of a source — the GitOps
// "sync this resource" action — without touching the rest of the project. It does
// not update the source's overall sync status (a partial sync isn't a project sync).
func (s *Service) SyncResource(ctx context.Context, workspaceID, id uint, kind, name string) (*apply.Result, error) {
	src, err := s.get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	manifests, _, err := s.fetch(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	return s.applier.ApplyResource(ctx, workspaceID, manifests, apply.Options{Prune: src.Prune, OwnerSource: sourceLabel(src)}, kind, name)
}

// DeleteResource deletes a single live resource (kind/name) managed by a source.
// Under auto-sync it will be recreated on the next reconcile — the handler warns
// the user. Scoped to the source so a caller can't delete outside its project.
func (s *Service) DeleteResource(ctx context.Context, workspaceID, id uint, kind, name string) (*apply.Result, error) {
	if _, err := s.get(workspaceID, id); err != nil {
		return nil, err
	}
	return s.applier.DeleteResource(ctx, workspaceID, kind, name)
}

// SyncByID reconciles a source by id without workspace scoping (worker/cron).
func (s *Service) SyncByID(ctx context.Context, id uint) error {
	src, err := s.repo.FindByID(id)
	if err != nil {
		return ErrNotFound
	}
	return s.Reconcile(ctx, src)
}

// ReconcileAuto reconciles every auto-sync source. It is the cron sweep entry
// point; errors on individual sources are logged, not fatal.
func (s *Service) ReconcileAuto(ctx context.Context) error {
	sources, err := s.repo.ListAuto()
	if err != nil {
		return err
	}
	for i := range sources {
		if err := s.Reconcile(ctx, &sources[i]); err != nil {
			logger.Warn("gitops auto-sync failed", "source", sources[i].ID, "name", sources[i].Name, "error", err)
		}
	}
	return nil
}

// Reconcile clones the source at its ref, applies the manifests, and records
// the resulting status on the source.
func (s *Service) Reconcile(ctx context.Context, src *models.GitSource) error {
	src.Status = models.GitSourceProgressing
	src.Message = ""
	_ = s.repo.Update(src)

	manifests, commit, err := s.fetch(ctx, src)
	if err != nil {
		return s.markError(src, fmt.Errorf("fetch: %w", err))
	}
	res, err := s.applier.Apply(ctx, src.WorkspaceID, manifests, apply.Options{Prune: src.Prune, OwnerSource: sourceLabel(src)})
	if err != nil {
		return s.markError(src, err)
	}
	if len(res.Failures) > 0 {
		return s.markError(src, fmt.Errorf("%d change(s) failed: %s", len(res.Failures), res.Failures[0].Error))
	}

	now := time.Now()
	src.Status = models.GitSourceSynced
	src.Message = ""
	src.LastSyncedCommit = commit.Hash
	src.LastSyncedAuthor = commit.Author
	src.LastSyncedSubject = commit.Subject
	src.LastSyncedAt = &now
	return s.repo.Update(src)
}

func (s *Service) markError(src *models.GitSource, err error) error {
	src.Status = models.GitSourceError
	src.Message = err.Error()
	_ = s.repo.Update(src)
	return err
}

// commitInfo is the synced commit's identity, recorded on the source for display.
type commitInfo struct {
	Hash    string
	Author  string
	Subject string
}

// fetch clones the repo at the source's ref into a temp dir and parses the
// manifests under its path, returning the rendered manifest bundle and the
// resolved commit (hash, author, subject). The bundle is the concatenation of
// all manifest files; the apply engine re-parses it as one set.
func (s *Service) fetch(ctx context.Context, src *models.GitSource) ([]byte, commitInfo, error) {
	auth, url, err := s.auth(src)
	if err != nil {
		return nil, commitInfo{}, err
	}
	dir, err := os.MkdirTemp("", "mb-gitops-")
	if err != nil {
		return nil, commitInfo{}, fmt.Errorf("create work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{URL: url, Auth: auth})
	if err != nil {
		return nil, commitInfo{}, fmt.Errorf("git clone: %w", err)
	}
	if ref := src.Ref; ref != "" && ref != "main" {
		hash, err := repo.ResolveRevision(plumbing.Revision(ref))
		if err != nil {
			return nil, commitInfo{}, fmt.Errorf("resolve ref %q: %w", ref, err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			return nil, commitInfo{}, err
		}
		if err := wt.Checkout(&git.CheckoutOptions{Hash: *hash}); err != nil {
			return nil, commitInfo{}, fmt.Errorf("checkout %q: %w", ref, err)
		}
	}
	var ci commitInfo
	if head, err := repo.Head(); err == nil {
		ci.Hash = head.Hash().String()
		if c, cerr := repo.CommitObject(head.Hash()); cerr == nil {
			ci.Author = c.Author.Name
			ci.Subject = strings.SplitN(strings.TrimSpace(c.Message), "\n", 2)[0]
		}
	}

	// Parse all manifests under the source path into a single set, then
	// re-serialize so the apply engine consumes one canonical bundle.
	set, err := declarative.ParseFS(os.DirFS(dir), src.Path)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// A missing path is always a configuration error — never a desired
			// state — so it must never trigger a prune.
			return nil, commitInfo{}, fmt.Errorf("manifest path %q not found at ref %q", srcPath(src.Path), refLabel(src.Ref))
		case errors.Is(err, declarative.ErrNoResources):
			// The path exists but holds no manifests. By default refuse, so a wiped
			// or wrong directory can't tear everything down. With AllowEmpty, treat
			// it as an intentional teardown: an empty bundle prunes all managed
			// resources (only meaningful when Prune is also on).
			if src.AllowEmpty {
				return []byte{}, ci, nil
			}
			return nil, commitInfo{}, fmt.Errorf("no miabi.io/v1 manifests found under %q — refusing to remove resources (enable \"Allow empty\" on the source to tear it down)", srcPath(src.Path))
		default:
			return nil, commitInfo{}, err
		}
	}
	bundle, err := declarative.Marshal(set)
	if err != nil {
		return nil, commitInfo{}, err
	}
	return bundle, ci, nil
}

// srcPath / refLabel give human-friendly defaults for error messages.
func srcPath(p string) string {
	if p == "" {
		return "."
	}
	return p
}

func refLabel(r string) string {
	if r == "" {
		return "main"
	}
	return r
}

// auth resolves the transport auth method and effective URL for a source.
func (s *Service) auth(src *models.GitSource) (transport.AuthMethod, string, error) {
	url := src.RepoURL
	if src.GitRepositoryID == nil {
		return nil, url, nil
	}
	gr, err := s.gitRepos.FindInWorkspace(src.WorkspaceID, *src.GitRepositoryID)
	if err != nil {
		return nil, "", fmt.Errorf("git credential %d: %w", *src.GitRepositoryID, err)
	}
	auth, err := gitrepo.AuthFor(gr)
	if err != nil {
		return nil, "", err
	}
	if url == "" {
		url = gr.URL
	}
	return auth, url, nil
}

// VerifyWebhook checks an inbound push webhook's HMAC-SHA256 signature against
// the source's secret (GitHub's X-Hub-Signature-256 scheme). A bare secret
// match (GitLab's X-Gitlab-Token) is also accepted.
func (s *Service) VerifyWebhook(src *models.GitSource, signature string, body []byte) bool {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return false
	}
	if signature == src.WebhookSecret { // GitLab token style
		return true
	}
	mac := hmac.New(sha256.New, []byte(src.WebhookSecret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func (in *Input) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.RepoURL = strings.TrimSpace(in.RepoURL)
	in.Ref = strings.TrimSpace(in.Ref)
	in.Path = strings.TrimSpace(in.Path)
	if in.Ref == "" {
		in.Ref = "main"
	}
	if in.Path == "" {
		in.Path = "."
	}
	if in.SyncPolicy != models.GitSyncAuto {
		in.SyncPolicy = models.GitSyncManual
	}
}

// IDByUID resolves a git source's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }
