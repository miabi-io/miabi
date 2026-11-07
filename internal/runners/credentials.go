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

package runners

import (
	"fmt"
	"net/url"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
)

// Advisory: the short-lived token is what actually authorizes the push/pull.
const registryUsername = "miabi-job"

// Both are ephemeral APIKey rows, so a leaked value is useless once the run ends or expires.
type JobCredentials struct {
	RegistryUser  string
	RegistryToken string // MIABI_REGISTRY_TOKEN — registry read+write, this app's repo
	JobToken      string // MIABI_JOB_TOKEN — deploy, this app/run (empty if disabled)
	keyIDs        []uint // the minted APIKey ids, revoked on terminal
}

func (c *JobCredentials) Secrets() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, 2)
	for _, v := range []string{c.RegistryToken, c.JobToken} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Covers the raw and percent-encoded forms so printing the remote or .git/config shows the mask.
func sourceURLSecrets(sourceURL string) []string {
	u, err := url.Parse(sourceURL)
	if err != nil || u.User == nil {
		return nil
	}
	pw, ok := u.User.Password()
	if !ok || pw == "" {
		return nil
	}
	out := []string{pw}
	if enc := url.UserPassword("", pw).String()[1:]; enc != pw {
		out = append(out, enc)
	}
	return out
}

// APIKey rows so job credentials ride the same auth/RBAC/audit path as a user token.
type CredentialMinter struct {
	keys            *auth.APIKeyService
	jobTokenEnabled bool // MIABI_JOB_API_TOKEN_ENABLED
}

func NewCredentialMinter(keys *auth.APIKeyService, jobTokenEnabled bool) *CredentialMinter {
	return &CredentialMinter{keys: keys, jobTokenEnabled: jobTokenEnabled}
}

// Both expire at the job deadline; call Revoke when the run goes terminal.
func (m *CredentialMinter) Mint(subjectUserID, workspaceID uint, appID *uint, runID uint, deadline time.Time) (*JobCredentials, error) {
	creds := &JobCredentials{RegistryUser: registryUsername}

	// Read too: docker push HEADs each blob first, and a write-only token fails those checks.
	regTok, regKey, err := m.keys.CreateEphemeral(subjectUserID, workspaceID, appID,
		fmt.Sprintf("job:run-%d:registry", runID), []string{models.ScopeRegistryWrite, models.ScopeRegistryRead}, deadline)
	if err != nil {
		return nil, fmt.Errorf("mint registry token: %w", err)
	}
	creds.RegistryToken = regTok
	creds.keyIDs = append(creds.keyIDs, regKey.ID)

	if m.jobTokenEnabled {
		jobTok, jobKey, err := m.keys.CreateEphemeral(subjectUserID, workspaceID, appID,
			fmt.Sprintf("job:run-%d:deploy", runID), []string{models.ScopeDeploy}, deadline)
		if err != nil {
			m.Revoke(creds) // roll back the registry key so we don't leak a half-mint
			return nil, fmt.Errorf("mint job token: %w", err)
		}
		creds.JobToken = jobTok
		creds.keyIDs = append(creds.keyIDs, jobKey.ID)
	}
	return creds, nil
}

// They also expire at the deadline regardless (defense in depth).
func (m *CredentialMinter) Revoke(creds *JobCredentials) {
	if creds == nil {
		return
	}
	for _, id := range creds.keyIDs {
		_ = m.keys.Revoke(id)
	}
}
