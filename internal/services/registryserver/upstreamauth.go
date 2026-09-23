// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// The registry used to serve auth-less on the platform's private network, which made being on that
// network the whole of the authorization story. It now requires HTTP Basic even there, so reaching
// it is not the same as being allowed to use it: the gateway is the only client that holds the
// credential, and every tenant request still passes the forwardAuth check in front of it.
const (
	// upstreamUser names the single account in the registry's htpasswd file. It is not a person and
	// never appears in a tenant's `docker login` — those authenticate at the gateway.
	upstreamUser = "miabi-gateway"
	// upstreamTokenLabel keys the derived credential. Changing it rotates the password, which takes
	// effect when the registry is next recreated — never do so casually.
	upstreamTokenLabel = "registry:upstream-auth"

	// authVolume carries the htpasswd file. A volume of its own, not the data volume: the data
	// volume may live on S3, where there is nothing to mount.
	authVolume   = "mb-registry-auth"
	authPath     = "/auth"
	htpasswdName = "htpasswd"
	authRealm    = "Miabi internal registry"
)

// upstreamPassword is the credential the gateway presents to the registry. It derives from the
// master encryption key rather than being stored, so it survives a restart, is identical in the
// gateway config and the htpasswd file without a second source of truth, and never sits in the
// database. Resolved lazily so it does not depend on construction order relative to crypto.Init.
func (s *Service) upstreamPassword() string {
	return crypto.DeriveToken(upstreamTokenLabel)
}

// upstreamAuthHeader is the Authorization value the gateway sets on requests it forwards to the
// registry, replacing whatever the tenant's docker client sent — by then forwardAuth has already
// decided whether that tenant may proceed.
func (s *Service) upstreamAuthHeader() string {
	raw := upstreamUser + ":" + s.upstreamPassword()
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
}

// ensureUpstreamAuth writes the htpasswd file the registry authenticates against into its own
// volume, using image (already pulled for the registry itself) as the helper.
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

// htpasswdLine renders one bcrypt htpasswd entry. Distribution accepts only bcrypt, and bcrypt
// silently truncates at 72 bytes — the derived token is well under that, but a future longer
// credential would hash to the same value as its own prefix, so refuse rather than truncate.
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
