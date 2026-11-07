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

package registryserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"golang.org/x/crypto/bcrypt"
)

// The registry requires HTTP Basic even on the private network; only the gateway holds the credential.
const (
	// Not a person; tenants authenticate at the gateway, never with this account.
	upstreamUser = "miabi-gateway"
	// Changing this rotates the password on the next registry recreate; never do so casually.
	upstreamTokenLabel = "registry:upstream-auth"

	// Separate from the data volume, which may live on S3 where there is nothing to mount.
	authVolume   = "mb-registry-auth"
	authPath     = "/auth"
	htpasswdName = "htpasswd"
	authRealm    = "Miabi internal registry"
)

// Derived from the master key so gateway and htpasswd agree without storage; lazy to avoid crypto.Init ordering.
func (s *Service) upstreamPassword() string {
	return crypto.DeriveToken(upstreamTokenLabel)
}

// Replaces the tenant's Authorization; forwardAuth has already decided whether the tenant may proceed.
func (s *Service) upstreamAuthHeader() string {
	raw := upstreamUser + ":" + s.upstreamPassword()
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
}

func (s *Service) ensureUpstreamAuth(ctx context.Context, dc docker.Client, image string) error {
	if _, err := dc.CreateVolume(ctx, authVolume, docker.PlatformLabels(docker.RoleRegistry, docker.ManagedByMiabi, nil), 0); err != nil {
		return fmt.Errorf("ensure registry auth volume: %w", err)
	}
	line, err := htpasswdLine(upstreamUser, s.upstreamPassword())
	if err != nil {
		return err
	}
	if err := dc.CopyToVolume(ctx, authVolume, image, htpasswdName, strings.NewReader(line), int64(len(line))); err != nil {
		return fmt.Errorf("write registry htpasswd: %w", err)
	}
	return nil
}

// Distribution accepts only bcrypt, which silently truncates at 72 bytes, so refuse rather than truncate.
func htpasswdLine(user, password string) (string, error) {
	if len(password) > 72 {
		return "", fmt.Errorf("registry upstream password is %d bytes; bcrypt truncates above 72", len(password))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash registry upstream password: %w", err)
	}
	return user + ":" + string(hash) + "\n", nil
}

// upstreamAuthEnv turns on Basic auth in the registry container against the file above.
func upstreamAuthEnv() []string {
	return []string{
		"REGISTRY_AUTH=htpasswd",
		"REGISTRY_AUTH_HTPASSWD_REALM=" + authRealm,
		"REGISTRY_AUTH_HTPASSWD_PATH=" + authPath + "/" + htpasswdName,
	}
}
