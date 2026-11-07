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

// Package gitrepo manages encrypted Git credentials and builds the go-git auth shared by probes and deploys.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNameRequired   = errors.New("git repository name is required")
	ErrURLRequired    = errors.New("git repository URL is required")
	ErrSecretRequired = errors.New("git credential secret is required")
	ErrNameTaken      = errors.New("a git repository with this name already exists")
	// The name identifies the credential to apps and pipelines, so it can't change; edit DisplayName instead.
	ErrNameImmutable = errors.New("a git repository's name cannot be changed; edit its display name, or delete and recreate it")
	ErrNotFound      = errors.New("git repository not found")
	ErrUnknownSecret = errors.New("the referenced secret does not exist in this workspace")
)

// Passed explicitly to AuthFor/CredentialURL because the deploy worker, pipeline runner and GitOps each reach them.
type Secrets interface {
	CredentialSecret(workspaceID uint, enc, ref string) (string, error)
}

type Vault interface {
	Secrets
	ExistsByName(workspaceID uint, name string) bool
}

type Service struct {
	repo    *repositories.GitRepoRepository
	secrets Vault
	// Swappable so tests never turn a probe into an outbound request to a fixture URL.
	dial dialFunc
}

type dialFunc func(ctx context.Context, g *models.GitRepository) error

func NewService(repo *repositories.GitRepoRepository) *Service {
	s := &Service{repo: repo}
	s.dial = s.lsRemote
	return s
}

// SetDialer replaces the reachability check. Test-only seam.
func (s *Service) SetDialer(d dialFunc) { s.dial = d }

func (s *Service) SetSecrets(v Vault) { s.secrets = v }

// The two forms are mutually exclusive, so switching always clears the other.
func (s *Service) storeSecret(workspaceID uint, value string) (enc, ref string, err error) {
	if name := secret.RefName(value); name != "" {
		if s.secrets != nil && !s.secrets.ExistsByName(workspaceID, name) {
			return "", "", fmt.Errorf("%w: %q", ErrUnknownSecret, name)
		}
		return "", name, nil
	}
	enc, err = crypto.EncryptWS(workspaceID, value)
	return enc, "", err
}

type Input struct {
	Name        string
	DisplayName string
	URL         string
	AuthType    models.GitAuthType
	Username    string
	// A `${{ secrets.NAME }}` reference is resolved at every clone; blank on update keeps what is stored.
	Secret string
	// Non-nil replaces the stored values; a manifest sets them, the console leaves them alone.
	Metadata, Annotations models.Metadata
}

func (s *Service) Create(workspaceID uint, in Input) (*models.GitRepository, error) {
	name := slug.Make(in.Name, "")
	if name == "" {
		return nil, ErrNameRequired
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(in.Name)
	}
	if strings.TrimSpace(in.URL) == "" {
		return nil, ErrURLRequired
	}
	taken, err := s.repo.ExistsByName(workspaceID, name)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrNameTaken
	}
	authType := normalizeAuthType(in.AuthType)
	// Public repos (and a blank secret) are cloned anonymously — no stored secret.
	enc, ref := "", ""
	if authType != models.GitAuthPublic && strings.TrimSpace(in.Secret) != "" {
		enc, ref, err = s.storeSecret(workspaceID, in.Secret)
		if err != nil {
			return nil, err
		}
	}
	g := &models.GitRepository{
		WorkspaceID: workspaceID,
		Name:        name,
		DisplayName: displayName,
		URL:         normalizeGitURL(in.URL),
		AuthType:    authType,
		Username:    in.Username,
		Secret:      enc,
		SecretRef:   ref,
		Metadata:    in.Metadata,
		Annotations: in.Annotations,
	}
	if err := s.repo.Create(g); err != nil {
		return nil, err
	}
	// Not a precondition: a credential added before its repo exists is worth storing; the failure lands on the row.
	_ = s.Probe(context.Background(), g)
	return strip(g), nil
}

func (s *Service) Update(workspaceID, id uint, in Input) (*models.GitRepository, error) {
	g, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	// Only an actual rename is refused, so a client PATCHing the whole object back still works.
	if name := slug.Make(in.Name, ""); name != "" && name != g.Name {
		return nil, ErrNameImmutable
	}
	if dn := strings.TrimSpace(in.DisplayName); dn != "" {
		g.DisplayName = dn
	}
	// So a pure relabel does not trigger a probe.
	changed := false
	if in.URL != "" {
		if url := normalizeGitURL(in.URL); url != g.URL {
			g.URL, changed = url, true
		}
	}
	if in.AuthType != "" {
		if at := normalizeAuthType(in.AuthType); at != g.AuthType {
			g.AuthType, changed = at, true
		}
	}
	if in.Username != g.Username {
		changed = true
	}
	g.Username = in.Username
	switch {
	case g.AuthType == models.GitAuthPublic:
		if g.Secret != "" || g.SecretRef != "" {
			changed = true
		}
		g.Secret, g.SecretRef = "", "" // public repo carries no credential
	case strings.TrimSpace(in.Secret) != "":
		changed = true
		enc, ref, err := s.storeSecret(workspaceID, in.Secret)
		if err != nil {
			return nil, err
		}
		g.Secret, g.SecretRef = enc, ref
	}
	if in.Metadata != nil {
		g.Metadata = in.Metadata
	}
	if in.Annotations != nil {
		g.Annotations = in.Annotations
	}
	if err := s.repo.Update(g); err != nil {
		return nil, err
	}
	// A relabel should not cost a provider round trip or reset a known-good status to "checking".
	if changed {
		// The outcome lands on the row, so a failed check does not fail the update.
		_ = s.Probe(context.Background(), g)
	}
	return strip(g), nil
}

func (s *Service) Get(workspaceID, id uint) (*models.GitRepository, error) {
	g, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return strip(g), nil
}

// Safe because the name is immutable.
func (s *Service) GetByName(workspaceID uint, name string) (*models.GitRepository, error) {
	g, err := s.repo.FindByName(workspaceID, slug.Make(name, ""))
	if err != nil {
		return nil, ErrNotFound
	}
	return strip(g), nil
}

func (s *Service) List(workspaceID uint) ([]models.GitRepository, error) {
	repos, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range repos {
		strip(&repos[i])
	}
	return repos, nil
}

// BundleSecret prefers the vault reference so rotations survive; it re-reads because List/Get strip the secret.
func (s *Service) BundleSecret(workspaceID, id uint) (string, error) {
	g, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return "", ErrNotFound
	}
	if g.SecretRef != "" {
		return secret.Ref(g.SecretRef), nil
	}
	if g.Secret == "" {
		return "", nil
	}
	return crypto.Decrypt(g.Secret)
}

func (s *Service) Delete(workspaceID, id uint) error {
	g, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	return s.repo.Delete(g.ID)
}

// TestConnection performs an authenticated ls-remote against the repository.
func (s *Service) TestConnection(ctx context.Context, workspaceID, id uint) error {
	g, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	return s.Probe(ctx, g)
}

const probeTimeout = 15 * time.Second

// Probe persists the status either way; a failed probe never fails the call that saved the credential.
func (s *Service) Probe(ctx context.Context, g *models.GitRepository) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if s.dial == nil {
		s.dial = s.lsRemote
	}
	err := s.dial(ctx, g)
	now := time.Now().UTC()
	g.ConnectionCheckedAt = &now
	if err != nil {
		g.ConnectionStatus = models.GitConnectionFailed
		g.ConnectionError = connectionReason(err)
	} else {
		g.ConnectionStatus = models.GitConnectionOK
		g.ConnectionError = ""
	}
	// A targeted update, because Save would rewrite the row from a struct possibly stripped of its secret.
	if uerr := s.repo.SetConnection(g.ID, g.ConnectionStatus, g.ConnectionError, now); uerr != nil {
		logger.Warn("gitrepo: failed to record connection status", "id", g.ID, "error", uerr)
	}
	return err
}

// ls-remote is the cheapest request proving both that the host answers and that the credential authenticates.
func (s *Service) lsRemote(ctx context.Context, g *models.GitRepository) error {
	auth, err := AuthFor(g, s.secrets)
	if err != nil {
		return err
	}
	rem := gogit.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{Name: "origin", URLs: []string{g.URL}})
	if _, err := rem.ListContext(ctx, &gogit.ListOptions{Auth: auth}); err != nil {
		return fmt.Errorf("git connection failed: %w", err)
	}
	return nil
}

// Separates "host did not answer" from "it answered and said no", which decides fixing the URL vs the token.
func connectionReason(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out reaching the repository — check the URL and that the host is reachable from this server"
	case strings.Contains(msg, "authentication required"), strings.Contains(msg, "authorization failed"):
		return "authentication failed — the credential was rejected, or the repository is private and no credential is set"
	case strings.Contains(msg, "repository not found"):
		return "repository not found — check the URL, and that the credential can see it"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "dial tcp"):
		return "could not reach the host — check the URL and this server's network access"
	}
	return msg
}

// Unlike CredentialURL, the secret stays in the auth method rather than being embedded in the URL.
func (s *Service) CloneURLAuth(workspaceID uint, rawURL string, credentialID *uint) (string, transport.AuthMethod, error) {
	var g *models.GitRepository
	if credentialID != nil && *credentialID > 0 {
		found, err := s.repo.FindInWorkspace(workspaceID, *credentialID)
		if err != nil {
			return "", nil, ErrNotFound
		}
		g = found
		if strings.TrimSpace(rawURL) == "" {
			rawURL = g.URL
		}
	}
	if strings.TrimSpace(rawURL) == "" {
		return "", nil, ErrURLRequired
	}
	auth, err := AuthFor(g, s.secrets)
	if err != nil {
		return "", nil, err
	}
	return normalizeGitURL(rawURL), auth, nil
}

// Checkout is shared by the deploy worker and pipeline runner so they can't drift in resolving a revision.
func Checkout(ctx context.Context, dir, url, ref string, auth transport.AuthMethod, log func(string)) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", ErrURLRequired
	}
	logf := func(s string) {
		if log != nil {
			log(s)
		}
	}
	logf("cloning " + url)
	repo, err := gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{URL: url, Auth: auth})
	if err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}
	if strings.TrimSpace(ref) == "" {
		head, err := repo.Head()
		if err != nil {
			return "", fmt.Errorf("resolve HEAD: %w", err)
		}
		return head.Hash().String(), nil
	}
	hash, err := ResolveRef(repo, ref)
	if err != nil {
		return "", fmt.Errorf("resolve ref %q: %w", ref, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{Hash: *hash}); err != nil {
		return "", fmt.Errorf("checkout %q: %w", ref, err)
	}
	logf("checked out " + ref + " (" + hash.String()[:min(7, len(hash.String()))] + ")")
	return hash.String(), nil
}

func AuthFor(g *models.GitRepository, vault Secrets) (transport.AuthMethod, error) {
	secretValue, err := credentialSecret(g, vault)
	if err != nil || secretValue == "" {
		return nil, err
	}
	switch normalizeAuthType(g.AuthType) {
	case models.GitAuthSSH:
		user := g.Username
		if user == "" {
			user = "git"
		}
		keys, err := gitssh.NewPublicKeys(user, []byte(secretValue), "")
		if err != nil {
			return nil, fmt.Errorf("parse ssh key: %w", err)
		}
		return keys, nil
	default: // token / HTTPS basic auth
		user := g.Username
		if user == "" {
			user = "x-access-token" // provider-agnostic default for PAT auth
		}
		return &githttp.BasicAuth{Username: user, Password: secretValue}, nil
	}
}

// A vault reference with no vault wired is an error; a silent anonymous clone reads as "repository not found".
func credentialSecret(g *models.GitRepository, vault Secrets) (string, error) {
	if g == nil || normalizeAuthType(g.AuthType) == models.GitAuthPublic {
		return "", nil
	}
	if g.SecretRef != "" {
		if vault == nil {
			return "", fmt.Errorf("git credential %q references secret %q but the vault is unavailable", g.Name, g.SecretRef)
		}
		return vault.CredentialSecret(g.WorkspaceID, "", g.SecretRef)
	}
	if strings.TrimSpace(g.Secret) == "" {
		return "", nil
	}
	value, err := crypto.Decrypt(g.Secret)
	if err != nil {
		return "", fmt.Errorf("decrypt git secret: %w", err)
	}
	return value, nil
}

// Remote runners can't receive an SSH key via the URL; use an HTTPS token credential instead.
var ErrSSHUnsupportedOnRunner = errors.New("SSH-key git credentials can't be used for runner builds yet; add an HTTPS token credential for this repository")

// CredentialURL embeds the HTTPS credential for remote builders; it lands only in the ephemeral job workspace.
func CredentialURL(rawURL string, g *models.GitRepository, vault Secrets) (string, error) {
	rawURL = normalizeGitURL(rawURL)
	if g == nil || normalizeAuthType(g.AuthType) == models.GitAuthPublic {
		return rawURL, nil
	}
	if normalizeAuthType(g.AuthType) == models.GitAuthSSH {
		return "", ErrSSHUnsupportedOnRunner
	}
	secretValue, err := credentialSecret(g, vault)
	if err != nil {
		return "", err
	}
	if secretValue == "" {
		return rawURL, nil // nothing stored: an anonymous clone
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse git url %q: %w", rawURL, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("token auth requires an http(s) repository URL, got scheme %q", u.Scheme)
	}
	user := g.Username
	if user == "" {
		user = "x-access-token" // provider-agnostic default for PAT auth
	}
	u.User = url.UserPassword(user, secretValue) // url-encodes tokens with special chars
	return u.String(), nil
}

// A vault reference is a name, not a secret, so it stays for the UI.
func strip(g *models.GitRepository) *models.GitRepository {
	g.HasSecret = g.Secret != "" || g.SecretRef != ""
	g.Secret = ""
	return g
}

func normalizeAuthType(t models.GitAuthType) models.GitAuthType {
	switch t {
	case models.GitAuthSSH:
		return models.GitAuthSSH
	case models.GitAuthPublic:
		return models.GitAuthPublic
	default:
		return models.GitAuthToken
	}
}

func normalizeGitURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	if u == "" {
		return u
	}
	if !strings.HasSuffix(strings.ToLower(u), ".git") {
		u += ".git"
	}
	return u
}

// A clone has a local branch only for the default; others exist as origin/<name>, so that is tried too.
func ResolveRef(repo *gogit.Repository, ref string) (*plumbing.Hash, error) {
	ref = strings.TrimSpace(ref)
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err == nil {
		return hash, nil
	}
	if h, rerr := repo.ResolveRevision(plumbing.Revision("origin/" + strings.TrimPrefix(ref, "refs/heads/"))); rerr == nil {
		return h, nil
	}
	return nil, err
}

// RemoteCommit matches branch, then tag, then full ref name; a full commit hash is returned as is.
func RemoteCommit(ctx context.Context, url, ref string, auth transport.AuthMethod) (string, error) {
	ref = strings.TrimSpace(ref)
	if plumbing.IsHash(ref) {
		return ref, nil
	}
	rem := gogit.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{Name: "origin", URLs: []string{url}})
	refs, err := rem.ListContext(ctx, &gogit.ListOptions{Auth: auth, PeelingOption: gogit.AppendPeeled})
	if err != nil {
		return "", fmt.Errorf("git ls-remote: %w", err)
	}
	if hash, ok := matchRemoteRef(refs, ref); ok {
		return hash, nil
	}
	return "", fmt.Errorf("ref %q not found on the remote", ref)
}

// An annotated tag resolves to its peeled "^{}" commit, not the tag object.
func matchRemoteRef(refs []*plumbing.Reference, ref string) (string, bool) {
	byName := make(map[plumbing.ReferenceName]*plumbing.Reference, len(refs))
	for _, r := range refs {
		byName[r.Name()] = r
	}
	if ref == "" {
		head, ok := byName[plumbing.HEAD]
		if !ok {
			return "", false
		}
		if head.Type() == plumbing.SymbolicReference {
			target, ok := byName[head.Target()]
			if !ok {
				return "", false
			}
			return target.Hash().String(), true
		}
		return head.Hash().String(), true
	}
	candidates := []plumbing.ReferenceName{
		plumbing.NewBranchReferenceName(ref),
		plumbing.NewTagReferenceName(ref + "^{}"),
		plumbing.NewTagReferenceName(ref),
		plumbing.ReferenceName(ref + "^{}"),
		plumbing.ReferenceName(ref),
	}
	for _, name := range candidates {
		if r, ok := byName[name]; ok && r.Type() == plumbing.HashReference {
			return r.Hash().String(), true
		}
	}
	return "", false
}
