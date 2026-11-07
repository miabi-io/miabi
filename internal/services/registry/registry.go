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

// Package registry manages container-registry credentials for pulling private images; secrets are encrypted at rest.
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

// Optional; nil means literal secrets only.
type Vault interface {
	CredentialSecret(workspaceID uint, enc, ref string) (string, error)
	ExistsByName(workspaceID uint, name string) bool
}

type Service struct {
	repo    *repositories.RegistryRepository
	secrets Vault
}

func NewService(repo *repositories.RegistryRepository) *Service { return &Service{repo: repo} }

func (s *Service) SetSecrets(v Vault) { s.secrets = v }

// Secret is the only way the plaintext leaves this package.
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

// Reference and literal are mutually exclusive, so switching forms clears the other; blank is the caller's business.
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
	// A `${{ secrets.NAME }}` value is stored as a reference and resolved at every use; blank on update keeps the stored one.
	Secret string
	// Set by the declarative engine so a prune can tell its own credentials from hand-created ones; nil leaves them on update.
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
	// Rotate only when a new value is supplied.
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

// BundleSecret re-reads the row because List/Get strip the secret; references are kept so rotations survive.
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

// Manifests reference credentials by name (an Application's spec.registry).
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

// Fingerprints let the plan engine detect rotation without plaintext; undecryptable credentials are omitted, not rotations.
func (s *Service) Fingerprints(workspaceID uint) (map[string]string, error) {
	regs, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(regs))
	for i := range regs {
		// Fingerprint the reference, not the value: rotating the secret is not a change, repointing it is.
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

// Fingerprint is salted by name to prevent cross-registry correlation, and truncated since it is only compared.
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

// TestConnection follows the Docker Registry v2 token-auth flow.
func (s *Service) TestConnection(ctx context.Context, workspaceID, id uint) error {
	reg, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	// Resolve the vault reference so the test checks the credential deploys will actually use.
	password, err := s.Secret(reg)
	if err != nil {
		return err
	}
	return checkRegistryAuth(ctx, reg.Server, reg.Username, password)
}

// A vault reference is a name, not a secret, so it stays for the UI.
func strip(reg *models.Registry) *models.Registry {
	reg.HasSecret = reg.Secret != "" || reg.SecretRef != ""
	reg.Secret = ""
	return reg
}

func normalizeServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return DefaultServer
	}
	server = strings.TrimPrefix(server, "https://")
	server = strings.TrimPrefix(server, "http://")
	return strings.TrimSuffix(server, "/")
}

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
