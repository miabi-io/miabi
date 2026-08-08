// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package registry manages stored container-registry credentials used to pull
// private images at deploy time. Secrets are encrypted at rest.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNameRequired   = errors.New("registry name is required")
	ErrSecretRequired = errors.New("registry secret is required")
	ErrNameTaken      = errors.New("a registry with this name already exists")
	ErrNotFound       = errors.New("registry not found")
	ErrUnknownSecret  = errors.New("the referenced secret does not exist in this workspace")
)

// DefaultServer is the implicit registry when none is given (Docker Hub).
const DefaultServer = "registry-1.docker.io"

// Vault is the vault access a registry credential needs: resolving a stored credential — its own encrypted
// value, or the secret it references — and checking that a reference points at something. Implemented by the
// secret service; optional, with nil meaning literal secrets only.
type Vault interface {
	CredentialSecret(workspaceID uint, enc, ref string) (string, error)
	ExistsByName(workspaceID uint, name string) bool
}

type Service struct {
	repo    *repositories.RegistryRepository
	secrets Vault
}

func NewService(repo *repositories.RegistryRepository) *Service { return &Service{repo: repo} }

// SetSecrets wires the vault, enabling credentials that reference a Secret
// rather than storing their own copy of the password.
func (s *Service) SetSecrets(v Vault) { s.secrets = v }

// Secret resolves a credential's password: the vault value when it references a
// Secret, else its own decrypted literal. This is the only way the plaintext
// leaves this package.
func (s *Service) Secret(reg *models.Registry) (string, error) {
	if reg.SecretRef != "" {
		if s.secrets == nil {
			return "", fmt.Errorf("registry %q references secret %q but the vault is unavailable", reg.Name, reg.SecretRef)
		}
		return s.secrets.CredentialSecret(reg.WorkspaceID, "", reg.SecretRef)
	}
	if reg.Secret == "" {
		return "", nil
	}
	return crypto.Decrypt(reg.Secret)
}

// storeSecret decides where an incoming secret lands: a `${{ secrets.NAME }}` value becomes a vault reference,
// anything else is encrypted here. The two are mutually exclusive, so switching a credential from one form to
// the other always clears the other. A blank value means "leave as-is" and is the caller's business.
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

// Input describes a registry credential to create or update.
type Input struct {
	Name     string
	Server   string
	Username string
	// Secret is either the password itself (encrypted before storage) or a
	// `${{ secrets.NAME }}` reference to a workspace Secret, which is stored as a
	// reference and resolved at every use. Blank on update = keep what is stored.
	Secret string
	// Metadata and Annotations carry provenance labels and free-form notes. Set by the declarative apply engine
	// (managed-by, gitops-source) so a prune can tell its own credentials from hand-created ones; nil leaves them
	// untouched on update.
	Metadata    models.Metadata
	Annotations models.Metadata
}

func (s *Service) Create(workspaceID uint, in Input) (*models.Registry, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, ErrNameRequired
	}
	if strings.TrimSpace(in.Secret) == "" {
		return nil, ErrSecretRequired
	}
	taken, err := s.repo.ExistsByName(workspaceID, in.Name)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrNameTaken
	}
	enc, ref, err := s.storeSecret(workspaceID, in.Secret)
	if err != nil {
		return nil, err
	}
	reg := &models.Registry{
		WorkspaceID: workspaceID,
		Name:        in.Name,
		Server:      normalizeServer(in.Server),
		Username:    in.Username,
		Secret:      enc,
		SecretRef:   ref,
		Metadata:    in.Metadata,
		Annotations: in.Annotations,
	}
	if err := s.repo.Create(reg); err != nil {
		return nil, err
	}
	return strip(reg), nil
}

func (s *Service) Update(workspaceID, id uint, in Input) (*models.Registry, error) {
	reg, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if in.Name != "" {
		reg.Name = in.Name
	}
	reg.Server = normalizeServer(in.Server)
	reg.Username = in.Username
	// Only rotate the secret when a new value is supplied. Switching between a
	// stored password and a vault reference always clears the other form.
	if strings.TrimSpace(in.Secret) != "" {
		enc, ref, err := s.storeSecret(workspaceID, in.Secret)
		if err != nil {
			return nil, err
		}
		reg.Secret, reg.SecretRef = enc, ref
	}
	if in.Metadata != nil {
		reg.Metadata = in.Metadata
	}
	if in.Annotations != nil {
		reg.Annotations = in.Annotations
	}
	if err := s.repo.Update(reg); err != nil {
		return nil, err
	}
	return strip(reg), nil
}

// BundleSecret returns what a portable workspace bundle should carry for a credential: its vault reference
// when it has one, so the indirection and later rotations survive, else the decrypted password. It re-reads
// the row because List/Get strip the secret, and a listed record would export a blank credential.
func (s *Service) BundleSecret(workspaceID, id uint) (string, error) {
	reg, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return "", ErrNotFound
	}
	if reg.SecretRef != "" {
		return secret.Ref(reg.SecretRef), nil
	}
	if reg.Secret == "" {
		return "", nil
	}
	return crypto.Decrypt(reg.Secret)
}

// FindByName resolves a credential by its workspace-scoped name handle — how a
// manifest references one (an Application's spec.registry).
func (s *Service) FindByName(workspaceID uint, name string) (*models.Registry, error) {
	regs, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range regs {
		if regs[i].Name == name {
			return strip(&regs[i]), nil
		}
	}
	return nil, ErrNotFound
}

// Fingerprints returns a non-reversible fingerprint of every credential's stored password, keyed by registry
// name. It lets the declarative plan engine detect and converge a rotated password without the plaintext
// leaving this package. An undecryptable credential is omitted, so it reads as unknown, not as a rotation.
func (s *Service) Fingerprints(workspaceID uint) (map[string]string, error) {
	regs, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(regs))
	for i := range regs {
		// A vault-backed credential is fingerprinted by its *reference*, not by the value behind it: the
		// credential says "whatever secret X holds", so rotating X is not a change to this credential (the next
		// pull just reads the new value). Pointing it at a different secret is.
		if regs[i].SecretRef != "" {
			out[regs[i].Name] = Fingerprint(regs[i].Name, secret.Ref(regs[i].SecretRef))
			continue
		}
		plain, dErr := crypto.Decrypt(regs[i].Secret)
		if dErr != nil {
			continue
		}
		out[regs[i].Name] = Fingerprint(regs[i].Name, plain)
	}
	return out, nil
}

// Fingerprint derives the stable fingerprint of a registry password. It is salted with the credential's name,
// so the same password under two names does not produce the same digest — no cross-registry correlation, no
// shared rainbow table — and truncated, because it is only ever compared for equality.
func Fingerprint(name, password string) string {
	if password == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("miabi-registry-fp\x00" + name + "\x00" + password))
	return hex.EncodeToString(sum[:8])
}

func (s *Service) Get(workspaceID, id uint) (*models.Registry, error) {
	reg, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return strip(reg), nil
}

func (s *Service) List(workspaceID uint) ([]models.Registry, error) {
	regs, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range regs {
		strip(&regs[i])
	}
	return regs, nil
}

func (s *Service) Delete(workspaceID, id uint) error {
	reg, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	return s.repo.Delete(reg.ID)
}

// TestConnection verifies the stored credential can authenticate to the
// registry, following the Docker Registry v2 token-auth flow.
func (s *Service) TestConnection(ctx context.Context, workspaceID, id uint) error {
	reg, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	// Resolves the vault reference when there is one, so "Test connection" checks
	// the credential the deploy will actually use.
	password, err := s.Secret(reg)
	if err != nil {
		return err
	}
	return checkRegistryAuth(ctx, reg.Server, reg.Username, password)
}

// strip clears the ciphertext and flags secret presence for safe responses. A
// vault reference is a name, not a secret, so it is left in place for the UI.
func strip(reg *models.Registry) *models.Registry {
	reg.HasSecret = reg.Secret != "" || reg.SecretRef != ""
	reg.Secret = ""
	return reg
}

// normalizeServer trims a registry host to its bare authority, defaulting to
// Docker Hub when empty.
func normalizeServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return DefaultServer
	}
	server = strings.TrimPrefix(server, "https://")
	server = strings.TrimPrefix(server, "http://")
	return strings.TrimSuffix(server, "/")
}

// checkRegistryAuth probes the registry's /v2/ endpoint with basic auth and, if
// the registry uses bearer-token auth, completes a token request against the
// advertised realm.
func checkRegistryAuth(ctx context.Context, server, username, password string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	base := "https://" + normalizeServer(server)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/", nil)
	if err != nil {
		return err
	}
	if username != "" {
		req.SetBasicAuth(username, password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect to registry: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("registry returned status %d", resp.StatusCode)
	}

	// Bearer-token flow: parse the realm/service from WWW-Authenticate and
	// exchange basic creds for a token.
	challenge := resp.Header.Get("Www-Authenticate")
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return fmt.Errorf("authentication failed")
	}
	params := parseBearerChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return fmt.Errorf("authentication failed")
	}
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodGet, realm, nil)
	if err != nil {
		return err
	}
	q := tokenReq.URL.Query()
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	tokenReq.URL.RawQuery = q.Encode()
	if username != "" {
		tokenReq.SetBasicAuth(username, password)
	}
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		return fmt.Errorf("registry token request: %w", err)
	}
	defer func() { _ = tokenResp.Body.Close() }()
	if tokenResp.StatusCode != http.StatusOK {
		return fmt.Errorf("authentication failed (status %d)", tokenResp.StatusCode)
	}
	return nil
}

func parseBearerChallenge(challenge string) map[string]string {
	out := map[string]string{}
	rest := strings.TrimSpace(challenge[len("Bearer "):])
	for _, part := range strings.Split(rest, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[strings.TrimSpace(kv[0])] = strings.Trim(strings.TrimSpace(kv[1]), `"`)
	}
	return out
}
