// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"fmt"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
)

// registryUsername is the advisory docker username a job logs in with; the
// short-lived token is what actually authorizes the push/pull.
const registryUsername = "miabi-job"

// JobCredentials are the per-job, short-lived secrets injected into a runner job: a registry login
// scoped to this app's repo, and an optional callback token scoped to deploy just this app/run.
// Both are ephemeral APIKey rows, so a leaked value is useless once the run ends or expires.
type JobCredentials struct {
	RegistryUser  string
	RegistryToken string // MIABI_REGISTRY_TOKEN — registry read+write, this app's repo
	JobToken      string // MIABI_JOB_TOKEN — deploy, this app/run (empty if disabled)
	keyIDs        []uint // the minted APIKey ids, revoked on terminal
}

// Secrets returns the credential values that must be redacted from the run's
// live log stream (a step that echoes one prints ••••).
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

// CredentialMinter issues (and revokes) a run's ephemeral job credentials as
// APIKey rows, so they ride the exact same auth/RBAC/audit path as a user token.
type CredentialMinter struct {
	keys            *auth.APIKeyService
	jobTokenEnabled bool // MIABI_JOB_API_TOKEN_ENABLED
}

func NewCredentialMinter(keys *auth.APIKeyService, jobTokenEnabled bool) *CredentialMinter {
	return &CredentialMinter{keys: keys, jobTokenEnabled: jobTokenEnabled}
}

// Mint issues the per-job registry credential (registry_write, bound to the app) and, unless
// withheld by config, the MIABI_JOB_TOKEN callback. Both expire at the job deadline and are
// attributed to the run's subject user for audit. Call Revoke when the run goes terminal.
func (m *CredentialMinter) Mint(subjectUserID, workspaceID uint, appID *uint, runID uint, deadline time.Time) (*JobCredentials, error) {
	creds := &JobCredentials{RegistryUser: registryUsername}

	// Both write AND read: a `docker push` first HEADs each blob to skip layers the
	// registry already has, and those existence checks are reads. A write-only
	// token gets "pull requires a read scope" on the HEADs and the push fails.
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

// Revoke kills a run's minted credentials the moment the run reaches a terminal
// state. They also expire at the deadline regardless (defense in depth).
func (m *CredentialMinter) Revoke(creds *JobCredentials) {
	if creds == nil {
		return
	}
	for _, id := range creds.keyIDs {
		_ = m.keys.Revoke(id)
	}
}
