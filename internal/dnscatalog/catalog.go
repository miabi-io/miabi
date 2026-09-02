// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dnscatalog is the single source of truth for the DNS hosts Miabi can connect to. Each
// type has one Descriptor driving validation, secret handling, the API enum and the UI form.
package dnscatalog

import (
	"sort"
	"strings"
	"time"
)

const (
	FieldString   = "string"
	FieldPassword = "password"
	FieldTextarea = "textarea"
	FieldEnum     = "enum"
)

// Field is one credential input.
type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Secret      bool     `json:"secret,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
}

// Descriptor declares one DNS host.
type Descriptor struct {
	Type    string  `json:"type"`
	Label   string  `json:"label"`
	Fields  []Field `json:"fields"`
	DocsURL string  `json:"docs_url,omitempty"`
	// ChallengeOnly marks a host that solves DNS-01 but cannot own A/AAAA records.
	ChallengeOnly bool `json:"challenge_only,omitempty"`
	// PropagationTimeout raises the DNS-01 propagation cap for slow hosts. Zero uses the ACME
	// client's default; it is a ceiling, not a wait.
	PropagationTimeout time.Duration `json:"-"`
}

var registry = []Descriptor{
	{
		Type:    "cloudflare",
		Label:   "Cloudflare",
		DocsURL: "https://dash.cloudflare.com/profile/api-tokens",
		Fields: []Field{
			{Key: "api_token", Label: "API token", Type: FieldPassword, Required: true, Secret: true,
				Help: "A token with Zone:DNS:Edit on the zones you want Miabi to manage."},
		},
	},
	{
		Type:    "digitalocean",
		Label:   "DigitalOcean",
		DocsURL: "https://cloud.digitalocean.com/account/api/tokens",
		Fields: []Field{
			{Key: "api_token", Label: "API token", Type: FieldPassword, Required: true, Secret: true,
				Help: "A personal access token with write scope."},
		},
	},
	{
		Type:    "route53",
		Label:   "AWS Route 53",
		DocsURL: "https://console.aws.amazon.com/iam/home#/security_credentials",
		Fields: []Field{
			{Key: "access_key_id", Label: "Access key ID", Type: FieldString, Required: true},
			{Key: "secret_access_key", Label: "Secret access key", Type: FieldPassword, Required: true, Secret: true},
			{Key: "region", Label: "Region", Type: FieldString, Placeholder: "us-east-1",
				Help: "Optional. Route 53 is global; this only selects the API endpoint."},
		},
	},
	{
		Type:    "hetzner",
		Label:   "Hetzner DNS",
		DocsURL: "https://dns.hetzner.com/settings/api-token",
		Fields: []Field{
			{Key: "api_token", Label: "API token", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:    "googleclouddns",
		Label:   "Google Cloud DNS",
		DocsURL: "https://console.cloud.google.com/iam-admin/serviceaccounts",
		Fields: []Field{
			{Key: "project", Label: "Project ID", Type: FieldString, Required: true, Placeholder: "my-project"},
			{Key: "service_account_json", Label: "Service account JSON", Type: FieldTextarea, Required: true, Secret: true, Help: "The full JSON key for a service account with the DNS Administrator role."},
		},
	},
	{
		Type:    "azure",
		Label:   "Azure DNS",
		DocsURL: "https://portal.azure.com/",
		Fields: []Field{
			{Key: "subscription_id", Label: "Subscription ID", Type: FieldString, Required: true},
			{Key: "resource_group", Label: "Resource group", Type: FieldString, Required: true},
			{Key: "tenant_id", Label: "Tenant ID", Type: FieldString, Required: true},
			{Key: "client_id", Label: "Client ID", Type: FieldString, Required: true},
			{Key: "client_secret", Label: "Client secret", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:    "linode",
		Label:   "Linode / Akamai",
		DocsURL: "https://cloud.linode.com/profile/tokens",
		Fields: []Field{
			{Key: "api_token", Label: "API token", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:               "godaddy",
		Label:              "GoDaddy",
		DocsURL:            "https://developer.godaddy.com/keys",
		PropagationTimeout: 60 * time.Second,
		Fields: []Field{
			{Key: "api_token", Label: "API token", Type: FieldPassword, Required: true, Secret: true, Help: "Formatted as key:secret."},
		},
	},
	{
		Type:               "namecheap",
		Label:              "Namecheap",
		DocsURL:            "https://ap.www.namecheap.com/settings/tools/apiaccess/",
		PropagationTimeout: 120 * time.Second,
		Fields: []Field{
			{Key: "api_key", Label: "API key", Type: FieldPassword, Required: true, Secret: true},
			{Key: "username", Label: "Username", Type: FieldString, Required: true},
			{Key: "client_ip", Label: "Client IP", Type: FieldString, Placeholder: "203.0.113.10", Help: "The public IP you allow-listed with Namecheap."},
			{Key: "api_endpoint", Label: "API endpoint", Type: FieldString, Placeholder: "https://api.namecheap.com/xml.response"},
		},
	},
	{
		Type:    "ovh",
		Label:   "OVHcloud",
		DocsURL: "https://api.ovh.com/createToken/",
		Fields: []Field{
			{Key: "endpoint", Label: "Endpoint", Type: FieldEnum, Required: true, Default: "ovh-eu", Options: []string{"ovh-eu", "ovh-ca", "ovh-us", "kimsufi-eu", "kimsufi-ca", "soyoustart-eu", "soyoustart-ca"}},
			{Key: "application_key", Label: "Application key", Type: FieldString, Required: true},
			{Key: "application_secret", Label: "Application secret", Type: FieldPassword, Required: true, Secret: true},
			{Key: "consumer_key", Label: "Consumer key", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:    "gandi",
		Label:   "Gandi",
		DocsURL: "https://admin.gandi.net/organizations/account/pat",
		Fields: []Field{
			{Key: "api_token", Label: "Personal access token", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:    "powerdns",
		Label:   "PowerDNS",
		DocsURL: "https://doc.powerdns.com/authoritative/http-api/index.html",
		Fields: []Field{
			{Key: "server_url", Label: "API URL", Type: FieldString, Required: true, Placeholder: "http://127.0.0.1:8081"},
			{Key: "server_id", Label: "Server ID", Type: FieldString, Default: "localhost", Placeholder: "localhost"},
			{Key: "api_token", Label: "API key", Type: FieldPassword, Required: true, Secret: true, Help: "The api-key from pdns.conf."},
		},
	},
	{
		Type:          "acmedns",
		Label:         "acme-dns",
		DocsURL:       "https://github.com/joohoi/acme-dns",
		ChallengeOnly: true,
		Fields: []Field{
			{Key: "server_url", Label: "Server URL", Type: FieldString, Required: true, Placeholder: "https://auth.acme-dns.io"},
			{Key: "username", Label: "Username", Type: FieldString, Required: true},
			{Key: "password", Label: "Password", Type: FieldPassword, Required: true, Secret: true},
			{Key: "subdomain", Label: "Subdomain", Type: FieldString, Required: true, Help: "The subdomain issued at registration; one connection serves one delegated domain."},
		},
	},
	{
		Type:    "scaleway",
		Label:   "Scaleway",
		DocsURL: "https://console.scaleway.com/iam/api-keys",
		Fields: []Field{
			{Key: "secret_key", Label: "Secret key", Type: FieldPassword, Required: true, Secret: true},
			{Key: "organization_id", Label: "Organization ID", Type: FieldString, Required: true},
		},
	},
	{
		Type:    "netcup",
		Label:   "netcup",
		DocsURL: "https://www.netcup-wiki.de/wiki/CCP_API",
		Fields: []Field{
			{Key: "customer_number", Label: "Customer number", Type: FieldString, Required: true},
			{Key: "api_key", Label: "API key", Type: FieldString, Required: true},
			{Key: "api_password", Label: "API password", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:    "infomaniak",
		Label:   "Infomaniak",
		DocsURL: "https://manager.infomaniak.com/v3/infomaniak-api",
		Fields: []Field{
			{Key: "api_token", Label: "API token", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:    "transip",
		Label:   "TransIP",
		DocsURL: "https://www.transip.nl/cp/account/api/",
		Fields: []Field{
			{Key: "account_name", Label: "Account name", Type: FieldString, Required: true},
			{Key: "private_key", Label: "Private key", Type: FieldTextarea, Required: true, Secret: true, Placeholder: "-----BEGIN PRIVATE KEY-----"},
		},
	},
	{
		Type:    "glesys",
		Label:   "GleSYS",
		DocsURL: "https://glesys.com/",
		Fields: []Field{
			{Key: "project", Label: "Project", Type: FieldString, Required: true, Placeholder: "CL12345"},
			{Key: "api_key", Label: "API key", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:    "cloudns",
		Label:   "ClouDNS",
		DocsURL: "https://www.cloudns.net/api-settings/",
		Fields: []Field{
			{Key: "auth_id", Label: "Auth ID", Type: FieldString, Help: "Use either an auth ID or a sub-auth ID."},
			{Key: "sub_auth_id", Label: "Sub-auth ID", Type: FieldString},
			{Key: "auth_password", Label: "Auth password", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:    "tencentcloud",
		Label:   "Tencent Cloud DNSPod",
		DocsURL: "https://console.cloud.tencent.com/cam/capi",
		Fields: []Field{
			{Key: "secret_id", Label: "Secret ID", Type: FieldString, Required: true},
			{Key: "secret_key", Label: "Secret key", Type: FieldPassword, Required: true, Secret: true},
			{Key: "region", Label: "Region", Type: FieldString, Placeholder: "ap-guangzhou"},
		},
	},
	{
		Type:    "huaweicloud",
		Label:   "Huawei Cloud DNS",
		DocsURL: "https://console.huaweicloud.com/iam/",
		Fields: []Field{
			{Key: "access_key_id", Label: "Access key ID", Type: FieldString, Required: true},
			{Key: "secret_access_key", Label: "Secret access key", Type: FieldPassword, Required: true, Secret: true},
			{Key: "region_id", Label: "Region", Type: FieldString, Placeholder: "cn-north-4"},
		},
	},
	{
		Type:    "bunny",
		Label:   "Bunny.net",
		DocsURL: "https://dash.bunny.net/account/settings",
		Fields: []Field{
			{Key: "access_key", Label: "API access key", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:    "luadns",
		Label:   "LuaDNS",
		DocsURL: "https://api.luadns.com/settings",
		Fields: []Field{
			{Key: "email", Label: "Email", Type: FieldString, Required: true},
			{Key: "api_key", Label: "API key", Type: FieldPassword, Required: true, Secret: true},
		},
	},
	{
		Type:               "namesilo",
		Label:              "NameSilo",
		DocsURL:            "https://www.namesilo.com/account/api-manager",
		PropagationTimeout: 900 * time.Second,
		Fields: []Field{
			{Key: "api_token", Label: "API key", Type: FieldPassword, Required: true, Secret: true},
		},
	},
}

var byType = func() map[string]Descriptor {
	m := make(map[string]Descriptor, len(registry))
	for _, d := range registry {
		m[d.Type] = d
	}
	return m
}()

func Get(t string) (Descriptor, bool) {
	d, ok := byType[strings.ToLower(strings.TrimSpace(t))]
	return d, ok
}

// All returns every descriptor in display order.
func All() []Descriptor {
	out := make([]Descriptor, len(registry))
	copy(out, registry)
	return out
}

// Types returns every catalogued type, sorted, for OpenAPI descriptions and error messages.
func Types() []string {
	out := make([]string, 0, len(registry))
	for _, d := range registry {
		out = append(out, d.Type)
	}
	sort.Strings(out)
	return out
}

// Validate reports the first required field the credentials leave empty.
func (d Descriptor) Validate(creds map[string]string) error {
	for _, f := range d.Fields {
		if f.Required && strings.TrimSpace(creds[f.Key]) == "" {
			return &MissingFieldError{Type: d.Type, Field: f.Key, Label: f.Label}
		}
	}
	return nil
}

// Public filters credentials to the fields safe to return.
func (d Descriptor) Public(creds map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range d.Fields {
		if f.Secret {
			continue
		}
		if v, ok := creds[f.Key]; ok && v != "" {
			out[f.Key] = v
		}
	}
	return out
}

type MissingFieldError struct {
	Type  string
	Field string
	Label string
}

func (e *MissingFieldError) Error() string {
	return e.Type + ": " + e.Label + " (" + e.Field + ") is required"
}
