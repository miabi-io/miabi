// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// gatewayWebPort is the edge gateway's published HTTP entry point (see
// RenderConfig entryPoints.web), where the reload endpoint is served.
const gatewayWebPort = 80

const reloadEndpointPath = "/gateway/reload"

// reloadHTTPClient is the client used for reload calls: a short timeout so a
// slow/unreachable node never blocks the caller for long.
var reloadHTTPClient = &http.Client{Timeout: 10 * time.Second}

// ReloadToken is the credential for a gateway's on-demand reload endpoint, derived from the node's
// gateway token rather than being it.
//
// They must differ. The gateway token authenticates the control plane's provider endpoint, whose
// response is the node's whole config — including middleware rules with their secrets decrypted.
// The reload call, by contrast, travels the node's own network as a plaintext bearer on port 80, so
// anyone on that path learns whatever it carries. Deriving keeps them in step without storing a
// second secret: a sniffed reload token buys a re-poll of a config the gateway was going to fetch
// anyway, and nothing else.
func ReloadToken(gatewayToken string) string {
	return crypto.DeriveTokenFrom(gatewayToken, reloadTokenLabel)
}

// reloadTokenLabel keys the derivation. Changing it rotates every node's reload token, which takes
// effect only once each gateway is redeployed with the new env — so never casually.
const reloadTokenLabel = "gateway:reload"

// Reload tells the edge gateway fronting srv to pull and apply its configuration immediately, instead of
// waiting for the poll interval. A no-op for a gateway watching the providers volume, which reloads on
// write — but a manager gateway switched to the HTTP provider does need the nudge.
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

// reloadHost is the address Miabi reaches a node's gateway at. A remote edge node is reached at its own
// address; the manager is not — its record carries none, and the control plane's own loopback is not the
// host's. The manager's gateway is the COMPOSE one, which carries a different container name from the
// per-node gateway Miabi deploys; using the node name here resolved to nothing and every manager reload
// failed silently into the poll interval.
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
