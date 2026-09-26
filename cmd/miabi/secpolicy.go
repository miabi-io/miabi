// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/services/secpolicy"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/worker"
	"gorm.io/gorm"
)

// wireDeployPortPolicy gives a worker's deploy handler the Security Center's publish-time host-port
// check. The deploy handler is built in both the standalone and the embedded worker, and a copy that
// missed this would publish ports the policy forbids.
func wireDeployPortPolicy(h *worker.DeployHandler, db *gorm.DB, ee enterprise.EE, cfg *config.Config) {
	h.SetPortPolicy(secpolicy.NewService(repositories.NewSecurityPolicyRepository(db),
		repositories.NewWorkspaceRepository(db), ee, cfg.SecurityPolicies))
}
