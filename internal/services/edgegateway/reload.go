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

package edgegateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

const gatewayWebPort = 80

const reloadEndpointPath = "/gateway/reload"

// Short timeout so an unreachable node never blocks the caller for long.
var reloadHTTPClient = &http.Client{Timeout: 10 * time.Second}

// ReloadToken is derived, not the gateway token: the reload travels in plaintext, the token fetches decrypted config.
func ReloadToken(gatewayToken string) string {
	return crypto.DeriveTokenFrom(gatewayToken, reloadTokenLabel)
}

// Changing it rotates every node's reload token, effective only as each gateway is redeployed.
const reloadTokenLabel = "gateway:reload"

// Reload is a no-op for gateways watching the providers volume, which reload on write.
func (s *Service) Reload(ctx context.Context, srv *models.Server, token string) error {
	if s.effectiveFileProvider(srv) {
		return nil
	}
	host, err := s.reloadHost(srv)
	if err != nil {
		return err
	}
	baseURL := "http://" + net.JoinHostPort(host, strconv.Itoa(gatewayWebPort))
	if err := postReload(ctx, reloadHTTPClient, baseURL, ReloadToken(token)); err != nil {
		return fmt.Errorf("edgegateway: reload %q: %w", srv.Name, err)
	}
	return nil
}

// The manager has no address and runs the compose gateway, so it is reached by that container name.
func (s *Service) reloadHost(srv *models.Server) (string, error) {
	if isManager(srv) {
		return CentralContainerName, nil
	}
	if srv == nil || srv.Address == "" {
		return "", fmt.Errorf("edgegateway: server has no address to reach its gateway")
	}
	return srv.Address, nil
}

func postReload(ctx context.Context, client *http.Client, baseURL, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+reloadEndpointPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gateway returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
