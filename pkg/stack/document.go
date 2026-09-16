// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIVersion is the only apiVersion an install document may carry. It is deliberately not the
// miabi.io/v1 of workspace resources: that schema publishes its kinds as a generated enum, so a
// ControlPlane inside it would read as something the apply API accepts, which it never will.
const APIVersion = "install.miabi.io/v1"

// KindControlPlane is the install document's only kind today.
const KindControlPlane = "ControlPlane"

// defaultDocumentName names the singleton. One install per host, so it identifies nothing and is
// carried only so the dialect matches miabi.io/v1 and `stack status` has something to print.
const defaultDocumentName = "miabi"

// apiVersionPrefix is what a document of a FUTURE version still shares with this one, which is how
// "you need a newer CLI" is told apart from "this is not an install document at all".
const apiVersionPrefix = "install.miabi.io/"

// ErrNoDocument is returned for input that parses as YAML but holds no install document.
var ErrNoDocument = errors.New("no ControlPlane document found")

// documentNameRe mirrors the workspace dialect's metadata.name rule.
var documentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Document is the install manifest's on-disk form. Manifest stays the internal representation: both
// this and the flat schema convert into it, so nothing downstream learns which shape was read.
type Document struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   DocumentMeta `yaml:"metadata,omitempty"`
	Spec       Spec         `yaml:"spec"`
}

// DocumentMeta identifies the install. There is one per host, so the name is decoration: it labels
// `miabi stack status` output and nothing else. Renaming it renames nothing.
type DocumentMeta struct {
	Name string `yaml:"name,omitempty"`
}

// Spec is the ControlPlane's desired state. Optional scalars that pin a console-owned setting are
// pointers: an emitted environment variable locks the field in the UI, so "unset" and "set to the
// zero value" have to stay distinguishable.
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

// Endpoints are the URLs the platform answers on. Control is where nodes, agents and runners dial
// back, and is separate from Web because a node on a private network may reach the control plane at
// an address the public panel URL never resolves to.
type Endpoints struct {
	Web     string `yaml:"web,omitempty"`
	Control string `yaml:"control,omitempty"`
}

// ACME is the certificate authority used for every acme-managed host on this install. Email is the
// contact Let's Encrypt sends expiry notices to, and doubles as the admin login when none is given.
type ACME struct {
	Email        string `yaml:"email,omitempty"`
	DirectoryURL string `yaml:"directoryUrl,omitempty"`
}

// ServerSpec is the control plane's own container: the image it runs, the read-only host /proc bind
// that lets the Nodes page report real host CPU and memory, and any variable Miabi reads that this
// document does not already model.
type ServerSpec struct {
	Image string `yaml:"image,omitempty"`
	// HostProc is a pointer because absent must mean on.
	HostProc  *bool             `yaml:"hostProc,omitempty"`
	DockerGid string            `yaml:"dockerGid,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
}

// ImageSpec pins a component that has nothing to configure but the image it runs.
type ImageSpec struct {
	Image string `yaml:"image,omitempty"`
}

// GatewaySpec is Goma Gateway: the image, the config file bind-mounted read-only from beside this
// manifest, and anything that config interpolates.
type GatewaySpec struct {
	Image  string `yaml:"image,omitempty"`
	Config string `yaml:"config,omitempty"`
	// ConfigSha is written by Miabi, never by the operator: it records the digest of the DEFAULT
	// config last written, which is what keeps a customized goma.yml from being overwritten.
	ConfigSha string            `yaml:"configSha,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
}

// RegistrySpec is the built-in OCI registry. The host anchors every image reference Miabi has
// recorded and decides which workspace owns an image, which is why the console shows it read-only.
type RegistrySpec struct {
	Enabled bool   `yaml:"enabled"`
	Host    string `yaml:"host,omitempty"`
	Storage string `yaml:"storage,omitempty"`
}

// AdminSpec is the first admin account. Only the address: the password is generated into
// spec.secrets, because this is an identity and that is a credential.
type AdminSpec struct {
	Email string `yaml:"email,omitempty"`
}

// SecretsSpec holds the install's secrets in plaintext, as the flat schema always has. The file is
// mode 0600 on a root-owned host, and anyone who can read it already holds the Docker socket.
type SecretsSpec struct {
	DBPassword    string `yaml:"dbPassword,omitempty"`
	RedisPassword string `yaml:"redisPassword,omitempty"`
	JWTSecret     string `yaml:"jwtSecret,omitempty"`
	EncryptionKey string `yaml:"encryptionKey,omitempty"`
	AdminPassword string `yaml:"adminPassword,omitempty"`
	// GomaConfigEncryptionKey is the one secret here that is safe to lose: the gateway config it
	// protects is rendered from the database on every sync, so rotating it costs a converge.
	GomaConfigEncryptionKey string `yaml:"gomaConfigEncryptionKey,omitempty"`
	RegistryPlatformToken   string `yaml:"registryPlatformToken,omitempty"`
}

// NetworkingSpec covers the two Docker fabrics and the address policy around them: the shared proxy
// network routed apps join, the private one the platform talks over, and the pools and ranges Miabi
// allocates from. Named as a topic rather than a plural because only two of its fields are networks.
type NetworkingSpec struct {
	Proxy     NetworkConfig `yaml:"proxy,omitempty"`
	Internal  NetworkConfig `yaml:"internal,omitempty"`
	Pool      PoolSpec      `yaml:"pool,omitempty"`
	HostPorts HostPortsSpec `yaml:"hostPorts,omitempty"`
	External  ExternalSpec  `yaml:"external,omitempty"`
	DNS       DNSSpec       `yaml:"dns,omitempty"`
}

// PoolSpec is the CIDR Miabi carves every managed network out of, handed to Docker as explicit IPAM
// so its own small default pools cannot exhaust. It must not overlap the proxy network, your LAN or
// a VPN.
type PoolSpec struct {
	CIDR         string `yaml:"cidr,omitempty"`
	SubnetPrefix int    `yaml:"subnetPrefix,omitempty"`
}

// HostPortsSpec bounds the host ports an application may ask to publish.
type HostPortsSpec struct {
	Min int `yaml:"min,omitempty"`
	Max int `yaml:"max,omitempty"`
}

// ExternalSpec is the wildcard domain one-click application URLs are published under on the default
// cluster. Setting it here pins it; leave it empty to manage it from Clusters.
type ExternalSpec struct {
	BaseDomain   string `yaml:"baseDomain,omitempty"`
	CertProvider string `yaml:"certProvider,omitempty"`
}

// DNSSpec paces the managed-DNS re-assert sweep.
type DNSSpec struct {
	ReconcileMinutes int `yaml:"reconcileMinutes,omitempty"`
}

// BackupSpec is the platform's own backup (Enterprise). Stating any of it pins the console's
// corresponding fields, which is the point on an install described by infrastructure-as-code.
type BackupSpec struct {
	Schedule          string            `yaml:"schedule,omitempty"`
	Destination       BackupDestination `yaml:"destination,omitempty"`
	Encryption        BackupEncryption  `yaml:"encryption,omitempty"`
	IncludeTenantData *bool             `yaml:"includeTenantData,omitempty"`
	Retention         BackupRetention   `yaml:"retention,omitempty"`
}

// BackupDestination is the S3-compatible target. Bucket, accessKey and secretKey are all-or-nothing:
// with any one missing the control plane ignores the whole block and leaves the console in charge.
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

// BackupEncryption seals the recovery point. The passphrase is NOT the platform's master key and
// must never be set to it: it protects the database that would otherwise be the only place it lived,
// and a real disaster is exactly when the in-platform copy is unreachable. includeIdentity seals the
// master key in, which is what a restore onto fresh hardware needs.
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

// LicenseSpec points at a signed Enterprise license on disk. It is installed only when the database
// holds none: a license added through the console takes precedence.
type LicenseSpec struct {
	File string `yaml:"file,omitempty"`
}

// InstallSettings carries what only the kinded document models. It is not serialized: the flat
// schema never had these, and the document writer maps them field by field.
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
}

// ParseDocument reads an install document. Unknown fields are refused — every other manifest parser
// in the platform does the same, and a silently ignored `acme_emial:` is an install with no ACME
// contact and no error.
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

// IsDocument reports whether b looks like a kinded install document rather than the flat schema. It
// keys off apiVersion alone, so a malformed document is still reported as one and gets its own error
// rather than "this is not a Miabi stack manifest".
func IsDocument(b []byte) bool {
	var probe struct {
		APIVersion string `yaml:"apiVersion"`
	}
	if err := yaml.Unmarshal(b, &probe); err == nil {
		return strings.TrimSpace(probe.APIVersion) != ""
	}
	// A document too broken to unmarshal is still a document. Answering "false" here would report a
	// YAML error as "this is not a Miabi stack manifest", which sends the operator looking at the
	// wrong thing entirely.
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

// Manifest converts the document into the internal representation. Every consumer downstream of
// Load sees a Manifest and never learns which on-disk shape produced it.
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
		},
	}
	// The document keeps the gateway key with the other secrets; the manifest keeps it where the
	// gateway and control-plane specs already read it from. One value either way, so the two sides
	// cannot disagree.
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
				Image:     m.Images.Miabi,
				HostProc:  copyBool(m.HostProc),
				DockerGid: m.DockerGID,
				Env:       copyEnv(m.Env),
			},
			Database: ImageSpec{Image: m.Images.Postgres},
			Cache:    ImageSpec{Image: m.Images.Redis},
			Gateway: GatewaySpec{
				Image:     m.Images.Gateway,
				Config:    m.Gateway.Config,
				ConfigSha: m.Gateway.ConfigSHA,
				Env:       gatewayEnv,
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
