package dnsprovider

import (
	"context"
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/dns"
	"github.com/miabi-io/miabi/internal/models"
)

// ErrConsoleNameTaken refuses a console push into a workspace where a provider made in Miabi already
// holds ConsoleProviderName: it is never adopted silently.
var ErrConsoleNameTaken = errors.New("a DNS provider named " + ConsoleProviderName + " already exists in this workspace")

// ConsoleInput is what the billing console pushes. An empty APIToken keeps the stored one.
type ConsoleInput struct {
	ServerURL string
	APIToken  string
	Zones     []string
}

// UpsertConsole creates or updates the workspace's console-managed provider. It skips the plan quota:
// the console sells the DNS hosting, not the workspace's own provider allowance.
func (s *Service) UpsertConsole(_ context.Context, workspaceID uint, in ConsoleInput) (*models.DNSProvider, error) {
	p, err := s.repo.FindByName(workspaceID, ConsoleProviderName)
	if err != nil {
		return nil, err
	}
	if p != nil && !p.ConsoleManaged() {
		return nil, ErrConsoleNameTaken
	}
	token := strings.TrimSpace(in.APIToken)
	if token == "" {
		if p == nil {
			return nil, ErrInvalidCredentials
		}
		creds, derr := decryptCreds(p.CredentialsEnc)
		if derr != nil {
			return nil, derr
		}
		token = creds["api_token"]
	}
	creds := dns.Credentials{"server_url": in.ServerURL, "api_token": token}
	if _, err := dns.Build("console", creds); err != nil {
		return nil, errors.Join(ErrInvalidCredentials, err)
	}
	enc, err := encryptCreds(workspaceID, creds)
	if err != nil {
		return nil, err
	}
	if p == nil {
		p = &models.DNSProvider{WorkspaceID: workspaceID, Name: ConsoleProviderName, DisplayName: "Miabi Console",
			Type: "console", ManagedBy: models.ManagedByConsole}
	}
	p.CredentialsEnc, p.Zones = enc, in.Zones
	p.Status, p.LastError = models.DNSProviderStatusOK, ""
	if p.ID == 0 {
		return p, s.repo.Create(p)
	}
	return p, s.repo.Update(p)
}

// ConsoleProvider returns the workspace's console-managed provider, or nil when it has none.
func (s *Service) ConsoleProvider(workspaceID uint) (*models.DNSProvider, error) {
	p, err := s.repo.FindByName(workspaceID, ConsoleProviderName)
	if err != nil || p == nil || !p.ConsoleManaged() {
		return nil, err
	}
	return p, nil
}

// DeleteConsole removes the workspace's console-managed provider; its domains fall back to manual
// DNS. It reports whether there was one.
func (s *Service) DeleteConsole(workspaceID uint) (bool, error) {
	p, err := s.ConsoleProvider(workspaceID)
	if err != nil || p == nil {
		return false, err
	}
	return true, s.repo.Delete(p.ID)
}

// DeleteAllConsole removes every console-managed provider, when the instance leaves the console.
func (s *Service) DeleteAllConsole() error { return s.repo.DeleteManagedBy(models.ManagedByConsole) }

// ConsoleProviderFor returns the id of the console-managed provider whose zones cover a domain name,
// for connecting a new domain to it.
func (s *Service) ConsoleProviderFor(workspaceID uint, name string) *uint {
	p, err := s.ConsoleProvider(workspaceID)
	if err != nil || p == nil || !p.CoversDomain(name) {
		return nil
	}
	return &p.ID
}
