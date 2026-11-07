// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Deliberately not miabi.io/v1, whose generated kind enum would make ControlPlane look apply-able.
const APIVersion = "install.miabi.io/v1"

// KindControlPlane is the install document's only kind today.
const KindControlPlane = "ControlPlane"

// One install per host, so the name identifies nothing; it is kept for dialect parity and `stack status`.
const defaultDocumentName = "miabi"

// Lets "you need a newer CLI" be told apart from "not an install document".
const apiVersionPrefix = "install.miabi.io/"

// ErrNoDocument is returned for input that parses as YAML but holds no install document.
var ErrNoDocument = errors.New("no ControlPlane document found")

// documentNameRe mirrors the workspace dialect's metadata.name rule.
var documentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Both this and the flat schema convert into Manifest, so nothing downstream learns which shape was read.
type Document struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   DocumentMeta `yaml:"metadata,omitempty"`
	Spec       Spec         `yaml:"spec"`
}

type DocumentMeta struct {
	Name string `yaml:"name,omitempty"`
}

// Pointers because an emitted env var locks the field in the UI, so "unset" and "zero" must differ.
type Spec struct {
	Domain      string         `yaml:"domain"`
	Endpoints   Endpoints      `yaml:"endpoints,omitempty"`
	ACME        ACME           `yaml:"acme,omitempty"`
	Server      ServerSpec     `yaml:"server,omitempty"`
	Database    ImageSpec      `yaml:"database,omitempty"`
	Cache       ImageSpec      `yaml:"cache,omitempty"`
	Gateway     GatewaySpec    `yaml:"gateway,omitempty"`
	Registry    RegistrySpec   `yaml:"registry,omitempty"`
	Admin       AdminSpec      `yaml:"admin,omitempty"`
	Secrets     SecretsSpec    `yaml:"secrets,omitempty"`
	Networking  NetworkingSpec `yaml:"networking,omitempty"`
	Backup      *BackupSpec    `yaml:"backup,omitempty"`
	License     LicenseSpec    `yaml:"license,omitempty"`
	RunnerImage string         `yaml:"runnerImage,omitempty"`
}

// Control is separate from Web because a private-network node may reach the control plane at another address.
type Endpoints struct {
	Web     string `yaml:"web,omitempty"`
	Control string `yaml:"control,omitempty"`
}

// Email doubles as the admin login when none is given.
type ACME struct {
	Email        string `yaml:"email,omitempty"`
	DirectoryURL string `yaml:"directoryUrl,omitempty"`
}

type ServerSpec struct {
	Image string `yaml:"image,omitempty"`
	// HostProc is a pointer because absent must mean on.
	HostProc  *bool  `yaml:"hostProc,omitempty"`
	DockerGid string `yaml:"dockerGid,omitempty"`
	// Defaults to the private network, the only one the gateway reaches it on.
	TrustedProxies []string          `yaml:"trustedProxies,omitempty"`
	Env            map[string]string `yaml:"env,omitempty"`
}

// ImageSpec pins a component that has nothing to configure but the image it runs.
type ImageSpec struct {
	Image string `yaml:"image,omitempty"`
}

type GatewaySpec struct {
	Image  string `yaml:"image,omitempty"`
	Config string `yaml:"config,omitempty"`
	// Written by Miabi only: the digest of the default config, which keeps a customized goma.yml from being overwritten.
	ConfigSha string            `yaml:"configSha,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	// Only connections from these may set the client IP via forwarded headers; unset when facing the internet directly.
	TrustedProxies []string `yaml:"trustedProxies,omitempty"`
	// Needs a GeoIP database beside goma.yml; without one the header is never set.
	CountryHeader bool `yaml:"countryHeader,omitempty"`
}

// The host anchors every recorded image ref and decides workspace ownership, hence read-only in the console.
type RegistrySpec struct {
	Enabled bool   `yaml:"enabled"`
	Host    string `yaml:"host,omitempty"`
	Storage string `yaml:"storage,omitempty"`
}

// Only the address: the password is a credential and is generated into spec.secrets.
type AdminSpec struct {
	Email string `yaml:"email,omitempty"`
}

// Plaintext, but the file is 0600 and anyone who can read it already holds the Docker socket.
type SecretsSpec struct {
	DBPassword    string `yaml:"dbPassword,omitempty"`
	RedisPassword string `yaml:"redisPassword,omitempty"`
	JWTSecret     string `yaml:"jwtSecret,omitempty"`
	EncryptionKey string `yaml:"encryptionKey,omitempty"`
	AdminPassword string `yaml:"adminPassword,omitempty"`
	// Safe to lose: the gateway config is re-rendered from the database on every sync.
	GomaConfigEncryptionKey string `yaml:"gomaConfigEncryptionKey,omitempty"`
	RegistryPlatformToken   string `yaml:"registryPlatformToken,omitempty"`
}

type NetworkingSpec struct {
	Proxy     NetworkConfig `yaml:"proxy,omitempty"`
	Internal  NetworkConfig `yaml:"internal,omitempty"`
	Pool      PoolSpec      `yaml:"pool,omitempty"`
	HostPorts HostPortsSpec `yaml:"hostPorts,omitempty"`
	External  ExternalSpec  `yaml:"external,omitempty"`
	DNS       DNSSpec       `yaml:"dns,omitempty"`
}

// Explicit IPAM so Docker's small default pools cannot exhaust; must not overlap the proxy network, LAN or VPN.
type PoolSpec struct {
	CIDR          string `yaml:"cidr,omitempty"`
	SubnetPrefix  int    `yaml:"subnetPrefix,omitempty"`
	IPv6ULAPrefix string `yaml:"ipv6UlaPrefix,omitempty"`
}

// HostPortsSpec bounds the host ports an application may ask to publish.
type HostPortsSpec struct {
	Min int `yaml:"min,omitempty"`
	Max int `yaml:"max,omitempty"`
}

// Setting it here pins it; leave it empty to manage it from Clusters.
type ExternalSpec struct {
	BaseDomain   string `yaml:"baseDomain,omitempty"`
	CertProvider string `yaml:"certProvider,omitempty"`
}

// DNSSpec paces the managed-DNS re-assert sweep.
type DNSSpec struct {
	ReconcileMinutes int `yaml:"reconcileMinutes,omitempty"`
}

// Stating any field pins the console's corresponding field.
type BackupSpec struct {
	Schedule          string            `yaml:"schedule,omitempty"`
	Destination       BackupDestination `yaml:"destination,omitempty"`
	Encryption        BackupEncryption  `yaml:"encryption,omitempty"`
	IncludeTenantData *bool             `yaml:"includeTenantData,omitempty"`
	Retention         BackupRetention   `yaml:"retention,omitempty"`
}

// All-or-nothing: with bucket, accessKey or secretKey missing the control plane ignores the block.
type BackupDestination struct {
	Endpoint       string `yaml:"endpoint,omitempty"`
	Bucket         string `yaml:"bucket,omitempty"`
	Region         string `yaml:"region,omitempty"`
	AccessKey      string `yaml:"accessKey,omitempty"`
	SecretKey      string `yaml:"secretKey,omitempty"`
	UseSSL         *bool  `yaml:"useSSL,omitempty"`
	ForcePathStyle *bool  `yaml:"forcePathStyle,omitempty"`
	Path           string `yaml:"path,omitempty"`
	DatabasePath   string `yaml:"databasePath,omitempty"`
	VolumePath     string `yaml:"volumePath,omitempty"`
}

// Never set the passphrase to the master key; includeIdentity seals the master key in for fresh-hardware restores.
type BackupEncryption struct {
	Passphrase      string `yaml:"passphrase,omitempty"`
	Encrypt         *bool  `yaml:"encrypt,omitempty"`
	IncludeIdentity *bool  `yaml:"includeIdentity,omitempty"`
}

// BackupRetention caps whole recovery points. Zero is unbounded.
type BackupRetention struct {
	Max  int `yaml:"max,omitempty"`
	Days int `yaml:"days,omitempty"`
}

// Installed only when the database holds none: a console-added license takes precedence.
type LicenseSpec struct {
	File string `yaml:"file,omitempty"`
}

// Not serialized: the flat schema never had these, and the document writer maps them field by field.
type InstallSettings struct {
	ACMEDirectoryURL      string
	RegistryStorage       string
	RegistryPlatformToken string
	Pool                  PoolSpec
	HostPorts             HostPortsSpec
	External              ExternalSpec
	DNS                   DNSSpec
	Backup                *BackupSpec
	License               LicenseSpec
	GatewayTrustedProxies []string
	GatewayCountryHeader  bool
	ServerTrustedProxies  []string
}

// Unknown fields are refused: a silently ignored typo like `acme_emial:` would be an install with no ACME contact.
func ParseDocument(b []byte) (*Document, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)

	var found *Document
	for {
		var d Document
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse install document: %w", err)
		}
		if d.APIVersion == "" && d.Kind == "" {
			continue // an empty document, e.g. a trailing ---
		}
		if err := d.validate(); err != nil {
			return nil, err
		}
		if found != nil {
			return nil, errors.New("more than one ControlPlane document: an install has exactly one")
		}
		doc := d
		found = &doc
	}
	if found == nil {
		return nil, ErrNoDocument
	}
	return found, nil
}

// Keys off apiVersion alone so a malformed document still gets its own error.
func IsDocument(b []byte) bool {
	var probe struct {
		APIVersion string `yaml:"apiVersion"`
	}
	if err := yaml.Unmarshal(b, &probe); err == nil {
		return strings.TrimSpace(probe.APIVersion) != ""
	}
	// Answering false would report a YAML error as "not a Miabi stack manifest".
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "apiVersion:") {
			return true
		}
	}
	return false
}

func (d *Document) validate() error {
	switch {
	case d.APIVersion == APIVersion:
	case strings.HasPrefix(d.APIVersion, apiVersionPrefix):
		return fmt.Errorf("manifest is %s but this miabi understands %s — upgrade the CLI",
			d.APIVersion, APIVersion)
	default:
		return fmt.Errorf("apiVersion must be %q, got %q", APIVersion, d.APIVersion)
	}
	if d.Kind != KindControlPlane {
		return fmt.Errorf("unknown kind %q — an install manifest holds a %s", d.Kind, KindControlPlane)
	}
	if d.Metadata.Name == "" {
		d.Metadata.Name = defaultDocumentName
	}
	if !documentNameRe.MatchString(d.Metadata.Name) {
		return fmt.Errorf("metadata.name %q must be lowercase letters, digits and hyphens, starting with a letter or digit",
			d.Metadata.Name)
	}
	return nil
}

// MarshalDocument renders a document with the indentation the rest of the platform's YAML uses.
func MarshalDocument(d *Document) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("encode install document: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode install document: %w", err)
	}
	return b.Bytes(), nil
}

func (d *Document) Manifest() *Manifest {
	m := &Manifest{
		Version: CurrentVersion,
		// It came from a document, so Save writes it back as one.
		kinded:          true,
		Domain:          d.Spec.Domain,
		WebURL:          d.Spec.Endpoints.Web,
		ACMEEmail:       d.Spec.ACME.Email,
		ControlURL:      d.Spec.Endpoints.Control,
		Network:         d.Spec.Networking.Proxy,
		InternalNetwork: d.Spec.Networking.Internal,
		Images: Images{
			Miabi:    d.Spec.Server.Image,
			Postgres: d.Spec.Database.Image,
			Redis:    d.Spec.Cache.Image,
			Gateway:  d.Spec.Gateway.Image,
			Runner:   d.Spec.RunnerImage,
		},
		Secrets: Secrets{
			DBPassword:    d.Spec.Secrets.DBPassword,
			RedisPassword: d.Spec.Secrets.RedisPassword,
			JWTSecret:     d.Spec.Secrets.JWTSecret,
			EncryptionKey: d.Spec.Secrets.EncryptionKey,
			AdminEmail:    d.Spec.Admin.Email,
			AdminPassword: d.Spec.Secrets.AdminPassword,
		},
		Registry: Registry{Enabled: d.Spec.Registry.Enabled, Host: d.Spec.Registry.Host},
		Gateway: Gateway{
			Config:    d.Spec.Gateway.Config,
			ConfigSHA: d.Spec.Gateway.ConfigSha,
			Env:       copyEnv(d.Spec.Gateway.Env),
		},
		Env:       copyEnv(d.Spec.Server.Env),
		DockerGID: d.Spec.Server.DockerGid,
		HostProc:  copyBool(d.Spec.Server.HostProc),
		Install: InstallSettings{
			ACMEDirectoryURL:      d.Spec.ACME.DirectoryURL,
			RegistryStorage:       d.Spec.Registry.Storage,
			RegistryPlatformToken: d.Spec.Secrets.RegistryPlatformToken,
			Pool:                  d.Spec.Networking.Pool,
			HostPorts:             d.Spec.Networking.HostPorts,
			External:              d.Spec.Networking.External,
			DNS:                   d.Spec.Networking.DNS,
			Backup:                copyBackup(d.Spec.Backup),
			License:               d.Spec.License,
			GatewayTrustedProxies: slices.Clone(d.Spec.Gateway.TrustedProxies),
			GatewayCountryHeader:  d.Spec.Gateway.CountryHeader,
			ServerTrustedProxies:  slices.Clone(d.Spec.Server.TrustedProxies),
		},
	}
	// Single value in either location, so the two sides cannot disagree.
	if k := d.Spec.Secrets.GomaConfigEncryptionKey; k != "" {
		if m.Gateway.Env == nil {
			m.Gateway.Env = map[string]string{}
		}
		m.Gateway.Env[gomaConfigEncryptionKey] = k
	}
	return m
}

// NewDocument converts a manifest into its kinded form.
func NewDocument(m *Manifest) *Document {
	gatewayEnv := copyEnv(m.Gateway.Env)
	key := gatewayEnv[gomaConfigEncryptionKey]
	delete(gatewayEnv, gomaConfigEncryptionKey)
	if len(gatewayEnv) == 0 {
		gatewayEnv = nil
	}

	return &Document{
		APIVersion: APIVersion,
		Kind:       KindControlPlane,
		Metadata:   DocumentMeta{Name: defaultDocumentName},
		Spec: Spec{
			Domain:    m.Domain,
			Endpoints: Endpoints{Web: m.WebURL, Control: m.ControlURL},
			ACME:      ACME{Email: m.ACMEEmail, DirectoryURL: m.Install.ACMEDirectoryURL},
			Server: ServerSpec{
				Image:          m.Images.Miabi,
				HostProc:       copyBool(m.HostProc),
				DockerGid:      m.DockerGID,
				TrustedProxies: slices.Clone(m.Install.ServerTrustedProxies),
				Env:            copyEnv(m.Env),
			},
			Database: ImageSpec{Image: m.Images.Postgres},
			Cache:    ImageSpec{Image: m.Images.Redis},
			Gateway: GatewaySpec{
				Image:          m.Images.Gateway,
				Config:         m.Gateway.Config,
				ConfigSha:      m.Gateway.ConfigSHA,
				Env:            gatewayEnv,
				TrustedProxies: slices.Clone(m.Install.GatewayTrustedProxies),
				CountryHeader:  m.Install.GatewayCountryHeader,
			},
			Registry: RegistrySpec{
				Enabled: m.Registry.Enabled,
				Host:    m.Registry.Host,
				Storage: m.Install.RegistryStorage,
			},
			Admin: AdminSpec{Email: m.Secrets.AdminEmail},
			Secrets: SecretsSpec{
				DBPassword:              m.Secrets.DBPassword,
				RedisPassword:           m.Secrets.RedisPassword,
				JWTSecret:               m.Secrets.JWTSecret,
				EncryptionKey:           m.Secrets.EncryptionKey,
				AdminPassword:           m.Secrets.AdminPassword,
				GomaConfigEncryptionKey: key,
				RegistryPlatformToken:   m.Install.RegistryPlatformToken,
			},
			Networking: NetworkingSpec{
				Proxy:     m.Network,
				Internal:  m.InternalNetwork,
				Pool:      m.Install.Pool,
				HostPorts: m.Install.HostPorts,
				External:  m.Install.External,
				DNS:       m.Install.DNS,
			},
			Backup:      copyBackup(m.Install.Backup),
			License:     m.Install.License,
			RunnerImage: m.Images.Runner,
		},
	}
}

func copyEnv(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyBool(in *bool) *bool {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func copyBackup(in *BackupSpec) *BackupSpec {
	if in == nil {
		return nil
	}
	out := *in
	out.IncludeTenantData = copyBool(in.IncludeTenantData)
	out.Destination.UseSSL = copyBool(in.Destination.UseSSL)
	out.Destination.ForcePathStyle = copyBool(in.Destination.ForcePathStyle)
	out.Encryption.Encrypt = copyBool(in.Encryption.Encrypt)
	out.Encryption.IncludeIdentity = copyBool(in.Encryption.IncludeIdentity)
	return &out
}
