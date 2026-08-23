// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gitrepo manages stored Git credentials used to clone private repositories at build time.
// Secrets (tokens or SSH keys) are encrypted at rest. It also builds the go-git auth method shared by the
// test-connection check and the deploy worker.
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
	// ErrNameImmutable rejects renaming a git repository. The name is how the
	// credential is identified everywhere it is referenced — by applications that
	// clone through it, and by pipelines that will bind to it as their source — so
	// it is an identity rather than a label. Edit DisplayName instead; to use a
	// different name, delete the repository and create it again.
	ErrNameImmutable = errors.New("a git repository's name cannot be changed; edit its display name, or delete and recreate it")
	ErrNotFound      = errors.New("git repository not found")
	ErrUnknownSecret = errors.New("the referenced secret does not exist in this workspace")
)

// Secrets resolves a stored credential's secret: its own encrypted value, or the current value of the
// workspace Secret it references. It is threaded explicitly into AuthFor/CredentialURL rather than held
// on the Service, because the deploy worker, pipeline runner and GitOps each reach those separately.
type Secrets interface {
	CredentialSecret(workspaceID uint, enc, ref string) (string, error)
}

// Vault is the fuller access the Service itself needs: resolution plus the
// existence check that validates a reference when a credential is saved.
type Vault interface {
	Secrets
	ExistsByName(workspaceID uint, name string) bool
}

type Service struct {
	repo    *repositories.GitRepoRepository
	secrets Vault
	// dial performs the reachability check. Swappable so tests can exercise the
	// status bookkeeping without reaching the network — and so a probe never turns
	// a unit test into an outbound request to whatever URL a fixture happens to
	// name.
	dial dialFunc
}

// dialFunc probes a remote with a credential, returning nil when it answers and
// authenticates.
type dialFunc func(ctx context.Context, g *models.GitRepository) error

func NewService(repo *repositories.GitRepoRepository) *Service {
	s := &Service{repo: repo}
	s.dial = s.lsRemote
	return s
}

// SetDialer replaces the reachability check. Test-only seam.
func (s *Service) SetDialer(d dialFunc) { s.dial = d }

// SetSecrets wires the vault, enabling credentials that reference a Secret
// rather than storing their own copy of the token or key.
func (s *Service) SetSecrets(v Vault) { s.secrets = v }

// storeSecret decides where an incoming secret lands: a `${{ secrets.NAME }}` value becomes a reference to
// the vault, anything else is encrypted here. The two are mutually exclusive, so switching a credential
// from one form to the other always clears the other.
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

// Input describes a git credential to create or update. Name is the desired
// unique slug handle; DisplayName is the free-text label (falls back to Name).
type Input struct {
	Name        string
	DisplayName string
	URL         string
	AuthType    models.GitAuthType
	Username    string
	// Secret is either the token or SSH private key itself, encrypted before storage, or a
	// `${{ secrets.NAME }}` reference to a workspace Secret, stored as a reference and resolved at every
	// clone. Blank on update means keep what is stored.
	Secret string
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
	}
	if err := s.repo.Create(g); err != nil {
		return nil, err
	}
	// Probe once, after saving. Deliberately not a precondition: a credential added
	// before its repository exists, or during a network blip, is still worth
	// storing — and refusing to save would leave the user with nothing and no
	// record of what they typed. The failure is recorded on the row, so it is
	// discarded here rather than failing the create.
	_ = s.Probe(context.Background(), g)
	return strip(g), nil
}

func (s *Service) Update(workspaceID, id uint, in Input) (*models.GitRepository, error) {
	g, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	// Compared against the stored value so a no-op passes through: only an actual
	// rename is refused, and a client PATCHing the whole object back still works.
	if name := slug.Make(in.Name, ""); name != "" && name != g.Name {
		return nil, ErrNameImmutable
	}
	if dn := strings.TrimSpace(in.DisplayName); dn != "" {
		g.DisplayName = dn
	}
	// changed tracks whether anything the connection check depends on moved, so a
	// pure relabel does not trigger a probe.
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
		// Switching between a stored token and a vault reference always clears the
		// other form.
		enc, ref, err := s.storeSecret(workspaceID, in.Secret)
		if err != nil {
			return nil, err
		}
		g.Secret, g.SecretRef = enc, ref
	}
	if err := s.repo.Update(g); err != nil {
		return nil, err
	}
	// Only re-probe when something the check actually depends on moved. Renaming
	// the label should not cost a round trip to the provider, and should not reset
	// a known-good status to "checking".
	if changed {
		// As on create: the outcome lands on the row, so a failed check does not
		// fail the update that saved a corrected credential.
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

// GetByName resolves a repository by its workspace-unique handle, for callers
// that reference one by name rather than id — a manifest, a CLI flag, a pipeline
// binding. Safe because the name is immutable.
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

// BundleSecret returns what a portable workspace bundle should carry for a credential: its vault reference
// when it has one, so the indirection and later rotations survive, else the decrypted token or key. It
// re-reads the row because List/Get strip the secret, and a listed record would export a blank.
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

// Probe checks whether the remote answers with this credential and records the
// result on the row. The returned error is the failure for a caller that wants
// to surface it; the status is persisted either way.
//
// Failures never propagate as a write error: the credential is already stored,
// and a probe that could not reach the network is not a reason to fail the call
// that saved it.
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
	// Persisted through a targeted update: Save would rewrite the whole row from a
	// struct the caller may have handed us stripped of its secret.
	if uerr := s.repo.SetConnection(g.ID, g.ConnectionStatus, g.ConnectionError, now); uerr != nil {
		logger.Warn("gitrepo: failed to record connection status", "id", g.ID, "error", uerr)
	}
	return err
}

// lsRemote is the real reachability check: a `git ls-remote`, the cheapest
// request that proves both that the host answers and that the credential
// authenticates.
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

// connectionReason turns a transport error into something a user can act on.
// go-git's messages are terse and the distinction that matters — "the host did
// not answer" vs "it answered and said no" — is exactly the one that decides
// whether to fix the URL or the token.
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

// CloneURLAuth resolves an explicit repository URL plus an optional stored credential into the normalized
// clone URL and a go-git auth method, for clones running in-process. Either argument may carry the URL.
// Unlike CredentialURL the secret stays in the auth method rather than being embedded in the URL.
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

// Checkout clones url into dir and checks out ref, returning the resolved commit hash; an empty ref uses
// the cloned HEAD. It is the shared clone-and-checkout path used by both the deploy worker's git build
// and the pipeline runner's workspace, so the two cannot drift in how they resolve a revision.
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
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
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

// AuthFor builds a go-git auth method from a stored credential, resolving its
// secret through vault. Returns nil auth (anonymous) when g is nil. Shared by
// the deploy worker, GitOps, and the test-connection check.
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

// credentialSecret resolves the token or key a credential authenticates with, from its own encrypted value or
// from the workspace Secret it references. Returns "" for an anonymous clone. A credential that references the
// vault with no vault wired is an error — a silent anonymous clone becomes a confusing "repository not found".
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

// ErrSSHUnsupportedOnRunner is returned when a repo authenticates by SSH key but
// the clone must happen on a remote runner (which has no way to receive the key
// via the URL). The user should add an HTTPS token credential instead.
var ErrSSHUnsupportedOnRunner = errors.New("SSH-key git credentials can't be used for runner builds yet; add an HTTPS token credential for this repository")

// CredentialURL returns rawURL with the repository's HTTPS credential embedded, so a remote builder with
// no local git auth can clone a private repo. A public repo returns the URL unchanged, and an SSH-key
// credential returns ErrSSHUnsupportedOnRunner. The credential lands only in the ephemeral job workspace.
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

// strip clears the ciphertext and flags secret presence for safe responses. A
// vault reference is a name, not a secret, so it is left in place for the UI.
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

// normalizeGitURL trims the URL and appends a ".git" suffix when missing, since
// users routinely forget it. SSH (git@…) and HTTPS URLs are both handled; an
// already-".git" URL is left as-is.
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
