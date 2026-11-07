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

// Package portbinding queues host-port requests for admin approval, since host ports are a node-wide shared resource.
package portbinding

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/secpolicy"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// A privileged workspace is bounded only by this, not the configured window.
const maxPortNumber = 65535

var (
	ErrNotFound       = errors.New("port binding not found")
	ErrAppRequired    = errors.New("application not found in workspace")
	ErrPortNotExposed = errors.New("the application does not expose this container port")
	ErrHostPortRange  = errors.New("host port is outside the allowed range")
	ErrHostPortTaken  = errors.New("host port is already in use")
	ErrNotPending     = errors.New("binding is not pending review")
	// ErrPolicyDenied is returned when the platform's host-port policy refuses a request.
	ErrPolicyDenied = secpolicy.ErrPortsDenied
)

// Policy decides host-port requests against the Security Center. Satisfied by *secpolicy.Service.
type Policy interface {
	CheckPortRequest(req secpolicy.PortRequest) secpolicy.PortDecision
}

// Checks live published ports too, including containers deployed outside Miabi.
type DockerClients interface {
	For(serverID uint) (docker.Client, error)
}

type Service struct {
	repo       *repositories.PortBindingRepository
	apps       *repositories.ApplicationRepository
	ports      *repositories.AppPortRepository
	workspaces *repositories.WorkspaceRepository
	docker     DockerClients
	servers    ServerLister
	notify     ReviewNotifier
	policy     Policy
	minPort    int
	maxPort    int
}

func NewService(repo *repositories.PortBindingRepository, apps *repositories.ApplicationRepository, ports *repositories.AppPortRepository, workspaces *repositories.WorkspaceRepository, minPort, maxPort int) *Service {
	return &Service{repo: repo, apps: apps, ports: ports, workspaces: workspaces, minPort: minPort, maxPort: maxPort}
}

// SetPolicy wires the Security Center host-port policy. Unset keeps Community behaviour.
func (s *Service) SetPolicy(p Policy) { s.policy = p }

// decide asks the policy about a request; with none wired, privileged workspaces skip review.
func (s *Service) decide(b *models.PortBinding, userID uint, privileged bool, action string) secpolicy.PortDecision {
	if s.policy == nil {
		d := secpolicy.PortDecision{Allowed: true, AutoApprove: privileged}
		if privileged {
			d.AutoApproveNote = "Auto-approved (privileged workspace)"
		}
		return d
	}
	return s.policy.CheckPortRequest(secpolicy.PortRequest{
		WorkspaceID: b.WorkspaceID, UserID: userID, HostPort: b.HostPort, Protocol: b.Protocol,
		Privileged: privileged, Action: action,
		Resource: fmt.Sprintf("app:%d %d/%s", b.ApplicationID, b.HostPort, b.Protocol),
	})
}

// SetDocker is optional; when nil, conflict checks use the binding table only.
func (s *Service) SetDocker(d DockerClients) { s.docker = d }

type RequestInput struct {
	ApplicationID uint
	ContainerPort int
	Protocol      string
	HostPort      int
}

func normProto(p string) string {
	if p == "udp" {
		return "udp"
	}
	return "tcp"
}

// Request auto-approves privileged workspaces only when the port is free, else ErrHostPortTaken.
func (s *Service) Request(workspaceID, userID uint, in RequestInput) (*models.PortBinding, error) {
	privileged := s.isPrivileged(workspaceID)
	b, err := s.newPendingBinding(workspaceID, userID, in, privileged)
	if err != nil {
		return nil, err
	}
	dec := s.decide(b, userID, privileged, "request")
	if !dec.Allowed {
		return nil, ErrPolicyDenied
	}

	if dec.AutoApprove {
		inUse, owner, cerr := s.tenantPortConflict(workspaceID, b.ServerID, b.HostPort, b.Protocol)
		if cerr != nil {
			return nil, cerr
		}
		if aerr := autoApprove(b, userID, inUse, dec.AutoApproveNote); aerr != nil {
			if owner != "" {
				return nil, fmt.Errorf("%w (in use by %s)", aerr, owner)
			}
			return nil, aerr
		}
	}
	if err := s.repo.Create(b); err != nil {
		return nil, err
	}
	// An approved binding only publishes on the next deploy — flag the app.
	if b.Status == models.PortBindingApproved {
		s.markAppRedeploy(b.ApplicationID)
	}
	s.notifyPending(b)
	return b, nil
}

// RequestImport files a host-port binding during stack or compose import.
func (s *Service) RequestImport(workspaceID, userID uint, in RequestInput) (binding *models.PortBinding, conflict string, err error) {
	privileged := s.isPrivileged(workspaceID)
	b, err := s.newPendingBinding(workspaceID, userID, in, privileged)
	if err != nil {
		return nil, "", err
	}
	// A refusal skips this port in the import summary rather than failing the whole import.
	dec := s.decide(b, userID, privileged, "import")
	if !dec.Allowed {
		return nil, "", ErrPolicyDenied
	}
	inUse, owner, cerr := s.tenantPortConflict(workspaceID, b.ServerID, b.HostPort, b.Protocol)
	if cerr != nil {
		return nil, "", cerr
	}
	switch {
	case inUse:
		conflict = owner
		b.ReviewNote = fmt.Sprintf("host port %d/%s already in use on the node by %s — remap or have an admin review", b.HostPort, b.Protocol, owner)
	case dec.AutoApprove:
		_ = autoApprove(b, userID, false, dec.AutoApproveNote)
	}
	if err := s.repo.Create(b); err != nil {
		return nil, "", err
	}
	return b, conflict, nil
}

func (s *Service) newPendingBinding(workspaceID, userID uint, in RequestInput, privileged bool) (*models.PortBinding, error) {
	app, err := s.apps.FindInWorkspace(workspaceID, in.ApplicationID)
	if err != nil {
		return nil, ErrAppRequired
	}
	proto := normProto(in.Protocol)
	declared, err := s.ports.ListByApp(app.ID)
	if err != nil {
		return nil, err
	}
	if !exposes(declared, in.ContainerPort, proto) {
		return nil, ErrPortNotExposed
	}
	hostPort, err := s.resolveHostPort(app.ServerID, in.HostPort, proto, privileged)
	if err != nil {
		return nil, err
	}
	return &models.PortBinding{
		WorkspaceID: workspaceID, ApplicationID: app.ID, ServerID: app.ServerID,
		ContainerPort: in.ContainerPort, Protocol: proto, HostPort: hostPort,
		Status: models.PortBindingPending, RequestedBy: userID,
	}, nil
}

func (s *Service) resolveHostPort(serverID uint, want int, proto string, privileged bool) (int, error) {
	if want == 0 {
		free, err := s.FindFreeHostPort(serverID, proto, 0)
		if err != nil {
			return 0, err
		}
		if free == 0 {
			return 0, fmt.Errorf("%w: no free host port in %d-%d", ErrHostPortTaken, s.minPort, s.maxPort)
		}
		return free, nil
	}
	if !hostPortAllowed(want, s.minPort, s.maxPort, privileged) {
		if privileged {
			return 0, fmt.Errorf("%w (1-%d)", ErrHostPortRange, maxPortNumber)
		}
		return 0, fmt.Errorf("%w (%d-%d)", ErrHostPortRange, s.minPort, s.maxPort)
	}
	return want, nil
}

// Privileged workspaces publish infrastructure on fixed ports (25, 53, 443), so only the port space bounds them.
func hostPortAllowed(port, minPort, maxPort int, privileged bool) bool {
	if port < 1 || port > maxPortNumber {
		return false
	}
	if privileged {
		return true
	}
	return port >= minPort && port <= maxPort
}

// SuggestHostPort is scoped to the workspace via the app.
func (s *Service) SuggestHostPort(workspaceID, appID uint, proto string, preferred int) (int, error) {
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return 0, ErrAppRequired
	}
	return s.FindFreeHostPort(app.ServerID, proto, preferred)
}

// FindFreeHostPort checks both the table and live ports, scanning up from preferred and wrapping; 0 when exhausted.
func (s *Service) FindFreeHostPort(serverID uint, proto string, preferred int) (int, error) {
	proto = normProto(proto)
	live := s.livePublishedPorts(serverID)
	free := func(p int) (bool, error) {
		if _, taken := live[portKey(p, proto)]; taken {
			return false, nil
		}
		inUse, err := s.repo.HostPortInUse(serverID, p, proto, 0)
		return !inUse, err
	}
	start := preferred
	if start < s.minPort || start > s.maxPort {
		start = s.minPort
	}
	for p := start; p <= s.maxPort; p++ {
		if ok, err := free(p); err != nil {
			return 0, err
		} else if ok {
			return p, nil
		}
	}
	for p := s.minPort; p < start; p++ {
		if ok, err := free(p); err != nil {
			return 0, err
		} else if ok {
			return p, nil
		}
	}
	return 0, nil
}

// The live Docker check catches ports held by containers deployed outside Miabi.
func (s *Service) hostPortConflict(serverID uint, hostPort int, proto string, excludeID uint) (bool, string, error) {
	inUse, err := s.repo.HostPortInUse(serverID, hostPort, proto, excludeID)
	if err != nil {
		return false, "", err
	}
	if inUse {
		return true, "an approved binding", nil
	}
	if owner, ok := s.livePublishedPorts(serverID)[portKey(hostPort, proto)]; ok {
		return true, owner, nil
	}
	return false, "", nil
}

// The owner is named only for the workspace's own containers; others belong to other tenants or the platform.
func (s *Service) tenantPortConflict(workspaceID, serverID uint, hostPort int, proto string) (bool, string, error) {
	inUse, owner, err := s.hostPortConflict(serverID, hostPort, proto, 0)
	if err != nil || !inUse {
		return inUse, owner, err
	}
	return true, tenantOwner(s.nodeContainers(serverID), owner, workspaceID), nil
}

// tenantOwner names owner only when it is a container labelled with workspaceID.
func tenantOwner(conts []docker.Container, owner string, workspaceID uint) string {
	for _, ct := range conts {
		if containerName(ct) == owner && ct.Labels[docker.LabelWorkspace] == strconv.FormatUint(uint64(workspaceID), 10) {
			return owner
		}
	}
	return "another workload on the node"
}

// nodeContainers lists the node's running containers; nil when it cannot be reached.
func (s *Service) nodeContainers(serverID uint) []docker.Container {
	if s.docker == nil {
		return nil
	}
	dc, err := s.docker.For(serverID)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conts, err := dc.ListContainers(ctx, false)
	if err != nil {
		return nil
	}
	return conts
}

// Empty when unwired or unreachable; the table-only check still applies.
func (s *Service) livePublishedPorts(serverID uint) map[string]string {
	ports, _ := s.inspectNode(serverID)
	return ports
}

// Conflict checks treat unreachable and empty alike, but the admin overview must not.
func (s *Service) inspectNode(serverID uint) (map[string]string, bool) {
	if s.docker == nil {
		return nil, false
	}
	dc, err := s.docker.For(serverID)
	if err != nil {
		return nil, false // node offline / no client: rely on the table
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conts, err := dc.ListContainers(ctx, false) // running only
	if err != nil {
		return nil, false
	}
	return publishedPorts(conts), true
}

func publishedPorts(conts []docker.Container) map[string]string {
	out := map[string]string{}
	for _, ct := range conts {
		name := containerName(ct)
		for _, p := range ct.Ports {
			if p.PublicPort == 0 {
				continue
			}
			out[portKey(int(p.PublicPort), normProto(p.Protocol))] = name
		}
	}
	return out
}

func portKey(hostPort int, proto string) string {
	return fmt.Sprintf("%d/%s", hostPort, normProto(proto))
}

// parsePortKey is portKey's inverse, for entries that exist only as a live port.
func parsePortKey(key string) (int, string) {
	port, proto, found := strings.Cut(key, "/")
	if !found {
		return 0, "tcp"
	}
	n := 0
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil {
		return 0, normProto(proto)
	}
	return n, normProto(proto)
}

func containerName(ct docker.Container) string {
	if len(ct.Names) > 0 {
		return strings.TrimPrefix(ct.Names[0], "/")
	}
	if ct.ID != "" {
		return ct.ID[:min(12, len(ct.ID))]
	}
	return "another container"
}

func autoApprove(b *models.PortBinding, reviewer uint, hostPortInUse bool, note string) error {
	if hostPortInUse {
		return ErrHostPortTaken
	}
	b.Status = models.PortBindingApproved
	b.ReviewedBy = &reviewer
	b.ReviewNote = note
	return nil
}

// An approved port is published only on the next deploy, so the UI shows "redeploy required"; no-op if undeployed.
func (s *Service) markAppRedeploy(appID uint) {
	app, err := s.apps.FindByID(appID)
	if err != nil || app.CurrentReleaseID == nil {
		return
	}
	_ = s.apps.SetRedeployRequired(appID, true)
}

// isPrivileged reports whether the workspace auto-approves port bindings.
func (s *Service) isPrivileged(workspaceID uint) bool {
	if s.workspaces == nil {
		return false
	}
	ws, err := s.workspaces.FindByID(workspaceID)
	return err == nil && ws.Privileged
}

func (s *Service) ListByApp(workspaceID, appID uint) ([]models.PortBinding, error) {
	all, err := s.repo.ListByApp(appID)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, b := range all {
		if b.WorkspaceID == workspaceID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *Service) ListByWorkspace(workspaceID uint) ([]models.PortBinding, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// Cancel removes a workspace's own binding (e.g. withdraw a request).
func (s *Service) Cancel(workspaceID, id uint) error {
	b, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	return s.repo.Delete(b.ID)
}

func (s *Service) ListByStatus(status models.PortBindingStatus) ([]models.PortBinding, error) {
	return s.repo.ListByStatus(status)
}

// Approve takes effect on the app's next deploy, after a node-wide conflict check.
func (s *Service) Approve(id, reviewerID uint, note string) (*models.PortBinding, error) {
	b, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if b.Status != models.PortBindingPending {
		return nil, ErrNotPending
	}
	inUse, owner, err := s.hostPortConflict(b.ServerID, b.HostPort, b.Protocol, b.ID)
	if err != nil {
		return nil, err
	}
	if inUse {
		if owner != "" {
			return nil, fmt.Errorf("%w (in use by %s)", ErrHostPortTaken, owner)
		}
		return nil, ErrHostPortTaken
	}
	b.Status = models.PortBindingApproved
	b.ReviewedBy = &reviewerID
	b.ReviewNote = note
	if err := s.repo.Update(b); err != nil {
		return nil, err
	}
	// The port publishes on the app's next deploy — flag it as needing one.
	s.markAppRedeploy(b.ApplicationID)
	return b, nil
}

func (s *Service) Reject(id, reviewerID uint, note string) (*models.PortBinding, error) {
	b, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if b.Status != models.PortBindingPending {
		return nil, ErrNotPending
	}
	b.Status = models.PortBindingRejected
	b.ReviewedBy = &reviewerID
	b.ReviewNote = note
	if err := s.repo.Update(b); err != nil {
		return nil, err
	}
	return b, nil
}

func exposes(ports []models.AppPort, containerPort int, proto string) bool {
	for _, p := range ports {
		if p.ContainerPort == containerPort && normProto(p.Protocol) == proto {
			return true
		}
	}
	return false
}

// ApprovedBinding is a published binding with the app it belongs to, for the Security Center.
type ApprovedBinding struct {
	models.PortBinding
	AppName string `json:"app_name"`
}

// ListApproved returns every approved binding with its app name.
func (s *Service) ListApproved() ([]ApprovedBinding, error) {
	list, err := s.repo.ListByStatus(models.PortBindingApproved)
	if err != nil {
		return nil, err
	}
	names := map[uint]string{}
	out := make([]ApprovedBinding, 0, len(list))
	for _, b := range list {
		name, ok := names[b.ApplicationID]
		if !ok {
			if app, aerr := s.apps.FindByID(b.ApplicationID); aerr == nil {
				name = app.Name
			}
			names[b.ApplicationID] = name
		}
		out = append(out, ApprovedBinding{PortBinding: b, AppName: name})
	}
	return out, nil
}

// RevokeApproved flags apps for redeploy, which is when ports stop being published; dryRun changes nothing.
func (s *Service) RevokeApproved(reviewerID uint, dryRun bool) ([]ApprovedBinding, error) {
	list, err := s.ListApproved()
	if err != nil || dryRun {
		return list, err
	}
	for i := range list {
		b := list[i].PortBinding
		b.Status = models.PortBindingRejected
		b.ReviewedBy = &reviewerID
		b.ReviewNote = "Revoked by platform policy"
		if err := s.repo.Update(&b); err != nil {
			return list[:i], err
		}
		s.markAppRedeploy(b.ApplicationID)
	}
	return list, nil
}
