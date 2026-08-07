// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package platformrestore rebuilds a Miabi platform onto a fresh host from a
// recovery point taken by services/platformbackup.
//
// It runs OUTSIDE the control plane — from `miabi restore`, on a bare machine
// with nothing but Docker. That constraint shapes everything here: there is no
// database to read settings from, no configuration beyond what the operator
// typed, and no admin session to authorize against. The S3 credentials needed to
// fetch the backup are themselves inside the backup, so the operator supplies
// them on the command line, and the sealed identity envelope (internal/dr)
// supplies the rest — above all the master encryption key, without which a
// restored database is a catalogue of secrets nobody can read.
package platformrestore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/platformstack"
	dbstorage "github.com/miabi-io/miabi/internal/storage"
	"github.com/miabi-io/miabi/internal/storage/blob"
)

const (
	defaultPgImage  = "jkaninda/pg-bkup:latest"
	defaultVolImage = "jkaninda/volume-bkup:latest"
	volumeMount     = "/data"

	// The control-plane database's name and role, fixed by the installer's
	// Postgres spec (POSTGRES_USER=miabi / POSTGRES_DB=miabi). A restore must use
	// the same values, because it is talking to the container that spec created.
	platformDBName = "miabi"
	platformDBUser = "miabi"
)

var (
	// ErrHostNotClean is returned when the target host already carries an install.
	ErrHostNotClean = errors.New("this host already has a Miabi install")
	// ErrKEKMismatch is the one that matters most: the key in the identity
	// envelope is not the key the recovery point was taken under.
	ErrKEKMismatch = errors.New("the encryption key in the identity envelope does not match this recovery point")
)

// Options are the operator's inputs to a restore.
type Options struct {
	S3   blob.Config
	Ref  string // recovery point to restore; empty selects the newest found
	Path string // object prefix the artifacts live under

	Passphrase string

	// Domain rehosts the restored platform. Required with Clone.
	Domain string
	// Clone mints a NEW install id instead of reusing the recovered one.
	//
	// Restoring keeps the install id, which is what keeps the Enterprise license
	// valid — correct when the original host is gone. It is wrong when the
	// original is still running: two live platforms sharing one install id break
	// licensing, and a clone that inherits production's hostnames would race it
	// for DNS and ACME orders. Cloning is therefore explicit, and costs a license.
	Clone bool

	// Image overrides the control-plane image written into the new manifest.
	Image string
	// ManifestPath is where the rebuilt stack manifest is written.
	ManifestPath string
	// DryRun stops after preflight and reports the plan.
	DryRun bool
}

// Plan is what a restore will do, resolved before anything is created.
type Plan struct {
	Ref           string   `json:"ref"`
	InstallID     string   `json:"install_id"`
	MiabiVersion  string   `json:"miabi_version"`
	SchemaVersion string   `json:"schema_version,omitempty"`
	Domain        string   `json:"domain"`
	DatabaseFile  string   `json:"database_file"`
	Volumes       []string `json:"volumes"`
	Encrypted     bool     `json:"encrypted"`
	Clone         bool     `json:"clone"`
	// NewInstallID is set during a --clone restore, once the new id is minted.
	NewInstallID string `json:"new_install_id,omitempty"`
	// MissingTenantData names tenant artifacts the manifest lists that are no
	// longer in the bucket. Not fatal — the platform restores without them — but
	// that data is gone, and the operator should meet the fact here.
	MissingTenantData []string `json:"missing_tenant_data,omitempty"`
	// Manual lists what the operator must still do afterwards. Naming it up front
	// is the difference between a recovery and a surprise.
	Manual []string `json:"manual"`

	identity *dr.Identity
	manifest *dr.Manifest
}

// Service performs the restore.
type Service struct {
	dc    docker.Client
	stack *platformstack.Service
	log   func(string, ...any)
	// pgImage/volImage let a test or an air-gapped install pin the helper images.
	pgImage  string
	volImage string
}

// New builds a restore service over a host Docker client and the stack manager.
func New(dc docker.Client, stack *platformstack.Service, log func(string, ...any)) *Service {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Service{dc: dc, stack: stack, log: log, pgImage: defaultPgImage, volImage: defaultVolImage}
}

// SetImages pins the backup helper images.
func (s *Service) SetImages(pg, vol string) {
	if pg != "" {
		s.pgImage = pg
	}
	if vol != "" {
		s.volImage = vol
	}
}

// Preflight resolves and validates a recovery point without touching the host.
//
// Everything that can be checked is checked here, because the alternative is
// discovering a wrong passphrase after the stack is half-built. Nothing below
// this function creates anything until Preflight has returned cleanly.
func (s *Service) Preflight(ctx context.Context, opts Options) (*Plan, error) {
	store, err := blob.New(opts.S3)
	if err != nil {
		return nil, err
	}

	ref := strings.TrimSpace(opts.Ref)
	if ref == "" {
		if ref, err = s.newestRef(ctx, store, opts.Path); err != nil {
			return nil, err
		}
		s.log("no --ref given; using the newest recovery point found: %s", ref)
	}
	if !dr.IsRef(ref) {
		return nil, fmt.Errorf("%q is not a recovery point ref (they start with %s)", ref, dr.RefPrefix)
	}

	// 1. The recovery point's own description of itself, from the backup root.
	man, err := readManifest(ctx, store, opts.Path, ref)
	if err != nil {
		return nil, err
	}
	if !man.IdentitySealed {
		return nil, fmt.Errorf("recovery point %s carries no identity envelope, so it cannot rebuild a platform on a fresh host: "+
			"there is nothing in it that carries the encryption key its secrets are encrypted under. It can still be restored "+
			"onto a host that has the original MIABI_ENCRYPTION_KEY. To make future recovery points restorable anywhere, set a "+
			"backup passphrase (MIABI_PLATFORM_BACKUP_PASSPHRASE) — it seals the key into each one", ref)
	}
	// Sealed, and nothing to open it with. Said here rather than by the CLI,
	// because only the recovery point knows whether a passphrase was ever needed.
	if strings.TrimSpace(opts.Passphrase) == "" {
		return nil, fmt.Errorf("recovery point %s is sealed with a backup passphrase: pass --passphrase-file, or set MIABI_BACKUP_PASSPHRASE", ref)
	}

	// 2. The identity envelope. Opening it proves the operator holds the
	// passphrase, and yields the master key everything in the dump is encrypted
	// under.
	sealed, err := store.GetBytes(ctx, dr.IdentityObject(opts.Path, ref))
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, fmt.Errorf("the identity envelope for %s is missing from the bucket", ref)
		}
		return nil, err
	}
	identity, err := dr.Open(sealed, opts.Passphrase)
	if err != nil {
		return nil, err
	}

	// 3. The envelope must belong to THIS recovery point. An envelope from before
	// a key rotation opens perfectly and restores a database nobody can read, so
	// the mismatch is caught here rather than after the platform is up.
	if man.KEKFingerprint != "" {
		if got := crypto.DeriveTokenFrom(identity.EncryptionKey, models.KEKFingerprintLabel); got != man.KEKFingerprint {
			return nil, fmt.Errorf("%w: the envelope opened, but its encryption key is not the one %s was taken under — restoring it would leave every secret in the platform undecryptable", ErrKEKMismatch, ref)
		}
	}

	// 4. Every artifact this restore will USE must actually be in the bucket.
	missingTenant, err := s.assertArtifactsPresent(ctx, store, man)
	if err != nil {
		return nil, err
	}

	// 5. Never restore into an older binary than the one that produced the dump:
	// the schema would be ahead of the code, and the failure is silent.
	if err := assertVersionForward(man.MiabiVersion); err != nil {
		return nil, err
	}

	// 6. The host must be clean. A fresh install onto an existing Postgres volume
	// can never work — the data directory keeps the password it was created with —
	// and the stack service already knows how to say so.
	if _, err := platformstack.Load(manifestPathOf(opts)); err == nil {
		return nil, fmt.Errorf("%w (%s): restore onto a clean host, or remove the existing install first", ErrHostNotClean, manifestPathOf(opts))
	}
	if err := s.stack.CheckOrphanedData(ctx); err != nil {
		return nil, err
	}
	conflicts, err := s.stack.CheckPorts(ctx)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		return nil, platformstack.PortConflictError(conflicts)
	}

	// 4. Clone safety. Reusing an install id across two live platforms breaks
	// licensing; inheriting production's hostnames would have the clone fight it
	// for DNS and certificates.
	if opts.Clone && strings.TrimSpace(opts.Domain) == "" {
		return nil, errors.New("--clone requires --domain: a clone must not answer on the hostnames the original still serves")
	}

	domain := strings.TrimSpace(opts.Domain)
	if domain == "" {
		domain = identity.Domain
	}
	if domain == "" {
		return nil, errors.New("the recovery point records no domain; pass --domain")
	}

	plan := &Plan{
		Ref:           ref,
		InstallID:     identity.InstallID,
		MiabiVersion:  man.MiabiVersion,
		SchemaVersion: man.SchemaVersion,
		Domain:        domain,
		DatabaseFile:  man.DatabaseArtifact().File,
		Encrypted:     man.Encrypted,
		Clone:         opts.Clone,
		identity:      identity,
		manifest:      man,
	}
	for _, a := range man.VolumeArtifacts() {
		plan.Volumes = append(plan.Volumes, a.Volume)
	}
	plan.Manual = s.manualSteps(identity, opts, domain)
	if len(missingTenant) > 0 {
		plan.MissingTenantData = missingTenant
		plan.Manual = append(plan.Manual, fmt.Sprintf(
			"%d tenant artifact(s) are not in the bucket and cannot be restored — the platform comes back without that data",
			len(missingTenant)))
	}
	return plan, nil
}

// readManifest fetches a recovery point's info file from the backup root.
func readManifest(ctx context.Context, store *blob.Store, root, ref string) (*dr.Manifest, error) {
	body, err := store.GetBytes(ctx, dr.ManifestObject(root, ref))
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, fmt.Errorf("no recovery point info file for %s under %q — check --from, or run `miabi recovery-points` to see what the bucket holds", ref, root)
		}
		return nil, err
	}
	return dr.DecodeManifest(body)
}

// assertArtifactsPresent checks the bucket really holds what the manifest claims.
// A manifest is a record of what happened when the backup ran; retention,
// lifecycle rules and human error all act on the bucket afterwards.
//
// Only artifacts THIS restore consumes are fatal: the control-plane dump and the
// platform volumes, which it writes in the next few minutes. Tenant data is
// restored much later, by reconcile, from the recovered database — so a missing
// tenant artifact costs that one workspace's data, not the platform. Refusing to
// rebuild a control plane over it would trade a recoverable outage for a total
// one. The missing ones are returned so the plan can name them.
func (s *Service) assertArtifactsPresent(ctx context.Context, store *blob.Store, man *dr.Manifest) (missingTenant []string, err error) {
	var missingCritical []string
	for _, a := range man.Artifacts {
		if a.Subject == dr.SubjectIdentity {
			continue // already fetched
		}
		key := man.ArtifactKey(a)
		found, checkErr := store.Exists(ctx, key)
		if checkErr != nil {
			return nil, fmt.Errorf("check %s: %w", key, checkErr)
		}
		if found {
			continue
		}
		switch a.Subject {
		case dr.SubjectTenantDatabase, dr.SubjectTenantVolume:
			missingTenant = append(missingTenant, tenantLabel(a)+" ("+key+")")
		default:
			missingCritical = append(missingCritical, key)
		}
	}
	if len(missingCritical) > 0 {
		return nil, fmt.Errorf("recovery point %s is missing artifacts this restore needs: %s",
			man.Ref, strings.Join(missingCritical, ", "))
	}
	return missingTenant, nil
}

// tenantLabel names a tenant artifact the way an operator refers to it.
func tenantLabel(a dr.Artifact) string {
	switch {
	case a.Database != "":
		return "database " + a.Workspace + "/" + a.Database
	case a.Volume != "":
		return "volume " + a.Volume
	default:
		return a.Subject
	}
}

// artifactKey is an artifact's full object key, computed by the manifest so the
// restore looks exactly where the backup wrote.
func artifactKey(man *dr.Manifest, a dr.Artifact) string { return man.ArtifactKey(a) }

// assertVersionForward refuses to restore a dump produced by a NEWER Miabi than
// this binary. Migrations only run forward: a schema from the future against
// older code fails in ways that look like data corruption, long after the
// restore reported success. Unparseable or dev versions are allowed through with
// a warning — refusing an operator's recovery over a version string would be
// worse than the risk.
func assertVersionForward(backupVersion string) error {
	have, ok1 := parseVersion(config.Version)
	want, ok2 := parseVersion(backupVersion)
	if !ok1 || !ok2 {
		return nil
	}
	for i := range want {
		if want[i] == have[i] {
			continue
		}
		if want[i] > have[i] {
			return fmt.Errorf("this recovery point was taken by Miabi %s but this binary is %s: upgrade to at least %s and restore again (migrations only run forward)",
				backupVersion, config.Version, backupVersion)
		}
		return nil
	}
	return nil
}

// parseVersion reads a "v1.2.3" / "1.2.3" triple. Anything else is not a version
// this can reason about.
func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".", 4)
	if len(parts) < 3 {
		return out, false
	}
	for i := 0; i < 3; i++ {
		// Drop any pre-release suffix on the patch component ("3-rc1").
		num := parts[i]
		if j := strings.IndexAny(num, "-+"); j >= 0 {
			num = num[:j]
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// manualSteps names what a restore cannot do for the operator. Reporting these is
// part of the feature: a recovery that silently leaves DNS pointing at a dead
// host has not recovered anything.
func (s *Service) manualSteps(identity *dr.Identity, opts Options, domain string) []string {
	steps := []string{
		fmt.Sprintf("point DNS for %s at this host, then let the gateway obtain certificates", domain),
		"re-enrol runners and remote nodes (their enrolment is bound to the host that is gone)",
	}
	if identity.RegistryStorage == models.RegistryStorageFilesystem {
		steps = append(steps, "images pushed to the built-in registry are NOT in this recovery point (it used local storage): apps with no Git source must be re-pushed")
	}
	if opts.Clone {
		steps = append(steps, "this clone has a new install id and needs its own Enterprise license")
	}
	return steps
}

// Restore executes a plan. Order is deliberate and is the heart of this package:
// data services → dump into an empty database → verify the key → volumes → the
// rest of the stack. Bringing the control plane up earlier would have it migrate
// and seed a database that is about to be overwritten.
func (s *Service) Restore(ctx context.Context, plan *Plan, opts Options) error {
	m, err := s.buildManifest(plan, opts)
	if err != nil {
		return err
	}
	path := manifestPathOf(opts)

	// Persist before creating anything: the manifest now holds the recovered
	// encryption key and freshly generated database passwords, and they exist
	// nowhere else. A converge that dies halfway must leave the operator able to
	// re-run rather than stranded with an unreadable Postgres.
	if err := platformstack.Save(path, m); err != nil {
		return err
	}
	s.log("wrote %s", path)

	s.log("starting data services")
	if err := s.stack.ConvergeData(ctx, m); err != nil {
		return fmt.Errorf("bring up data services: %w", err)
	}

	s.log("restoring the control-plane database from %s", plan.DatabaseFile)
	if err := s.restoreDatabase(ctx, m, plan, opts); err != nil {
		return err
	}

	for _, a := range plan.manifest.VolumeArtifacts() {
		s.log("restoring volume %s", a.Volume)
		if err := s.restoreVolume(ctx, opts, plan.manifest.VolumePrefix, a); err != nil {
			return fmt.Errorf("restore volume %s: %w", a.Volume, err)
		}
	}

	// Quiesce the platform before it can act on the restored state: DNS may still
	// point at the host this was recovered from, and a control plane that begins
	// reconciling and re-issuing certificates against it makes the outage worse.
	// Written directly into the restored database, because it must be true before
	// the control plane's first boot — there is no API to call yet.
	if err := s.markRestorePending(ctx, m, plan); err != nil {
		return err
	}

	s.log("starting the control plane and gateway")
	if err := s.stack.Converge(ctx, m); err != nil {
		return fmt.Errorf("converge stack: %w", err)
	}
	if err := platformstack.Save(path, m); err != nil {
		return err
	}
	return nil
}

// markRestorePending writes the quiesce marker and the recovery provenance into
// the restored database, using a one-shot psql against the same Postgres the
// dump just landed in. It runs before the control plane boots, so the platform
// comes up already knowing it is a recovery and not a normal start.
func (s *Service) markRestorePending(ctx context.Context, m *platformstack.Manifest, plan *Plan) error {
	note := fmt.Sprintf("restored from %s on %s", plan.Ref, time.Now().UTC().Format(time.RFC3339))
	sql := fmt.Sprintf(
		`INSERT INTO settings (key, value, type, created_at, updated_at) `+
			`VALUES ('%s', '%s', 'string', now(), now()) `+
			`ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();`,
		models.RestorePendingKey, sqlEscape(note))

	// A clone must not inherit the original's install id. The id is what the
	// license is issued against, so two live platforms sharing one is a licensing
	// conflict — and the clone would also report the original's identity to the
	// customer portal. Restoring after a real loss keeps the id, which is exactly
	// what keeps the license valid on the new hardware.
	if plan.Clone {
		newID := "mbi_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		plan.NewInstallID = newID
		sql += fmt.Sprintf(
			`UPDATE settings SET value = '%s', updated_at = now() WHERE key = '%s';`,
			sqlEscape(newID), dbstorage.InstallIDKey)
	}

	code, out, err := s.dc.RunOneShot(ctx, docker.RunSpec{
		Name:  "mb-dr-quiesce",
		Image: m.Images.Postgres,
		Env: []string{
			"PGPASSWORD=" + m.Secrets.DBPassword,
		},
		Entrypoint: []string{"psql"},
		Cmd: []string{
			"-h", platformstack.ContainerPostgres, "-U", platformDBUser, "-d", platformDBName,
			"-v", "ON_ERROR_STOP=1", "-c", sql,
		},
		Networks: []string{m.Network.Name},
		Labels:   map[string]string{docker.LabelManaged: "true"},
	})
	if err != nil || code != 0 {
		return fmt.Errorf("could not mark the platform as recovering (exit %d): %s", code, out)
	}
	return nil
}

// sqlEscape doubles single quotes for the literal above. The value is built from
// a ref this code generated and a timestamp, so this guards against a surprise
// rather than against an attacker — but a restore is a bad place to learn that
// an assumption was wrong.
func sqlEscape(s string) string { return strings.ReplaceAll(s, "'", "''") }

// buildManifest turns a recovered identity into a stack manifest for THIS host.
//
// The encryption key and JWT secret are carried over — that is the point. The
// database and Redis passwords are NOT: they belong to the data directories this
// restore just created, and reusing the originals would recreate the
// password-vs-data-directory trap the installer refuses elsewhere.
func (s *Service) buildManifest(plan *Plan, opts Options) (*platformstack.Manifest, error) {
	image := strings.TrimSpace(opts.Image)
	m := platformstack.Defaults(image)
	id := plan.identity

	m.Domain = plan.Domain
	if opts.Domain == "" || opts.Domain == id.Domain {
		// Same identity: keep the URLs exactly as they were, so issued tokens,
		// webhooks and node endpoints stay valid.
		m.WebURL = id.WebURL
		m.ControlURL = id.ControlURL
	}
	m.ACMEEmail = id.ACMEEmail
	if id.NetworkName != "" {
		m.Network.Name = id.NetworkName
	}
	if id.NetworkSubnet != "" {
		m.Network.Subnet = id.NetworkSubnet
	}
	if id.RegistryHost != "" {
		m.Registry.Host, m.Registry.Enabled = id.RegistryHost, true
	}

	m.Secrets.EncryptionKey = id.EncryptionKey
	m.Secrets.JWTSecret = id.JWTSecret
	// GenerateSecrets fills only what is empty, so the two above survive and the
	// database/Redis/admin credentials are minted fresh for this host.
	if err := m.GenerateSecrets(); err != nil {
		return nil, err
	}
	if err := m.Normalize(); err != nil {
		return nil, err
	}
	return m, nil
}

// restoreDatabase loads the dump into the freshly created, empty database.
func (s *Service) restoreDatabase(ctx context.Context, m *platformstack.Manifest, plan *Plan, opts Options) error {
	env := []string{
		"DB_HOST=" + platformstack.ContainerPostgres,
		"DB_PORT=5432",
		"DB_NAME=" + platformDBName,
		"DB_USERNAME=" + platformDBUser,
		"DB_PASSWORD=" + m.Secrets.DBPassword,
	}
	env = append(env, backup.S3Env(s3Config(opts.S3))...)
	if plan.Encrypted || strings.HasSuffix(plan.DatabaseFile, ".gpg") {
		if opts.Passphrase == "" {
			return errors.New("this recovery point is encrypted but no passphrase was supplied")
		}
		env = append(env, "GPG_PASSPHRASE="+opts.Passphrase)
	}

	cmd := []string{"restore", "--storage", "s3", "-d", platformDBName, "-f", plan.DatabaseFile}
	if p := strings.Trim(plan.manifest.Prefix, "/"); p != "" {
		cmd = append(cmd, "--path", p)
	}
	if err := s.dc.PullImage(ctx, s.pgImage, nil); err != nil {
		return fmt.Errorf("pull %s: %w", s.pgImage, err)
	}
	code, out, err := s.dc.RunOneShot(ctx, docker.RunSpec{
		Name:     "mb-dr-dbrestore",
		Image:    s.pgImage,
		Env:      env,
		Cmd:      cmd,
		Networks: []string{m.Network.Name},
		Labels:   map[string]string{docker.LabelManaged: "true"},
	})
	if err != nil || code != 0 {
		return fmt.Errorf("database restore exited %d: %s", code, out)
	}
	return nil
}

// restoreVolume recreates a platform volume and unpacks its archive into it.
//
// Encryption is decided per ARTIFACT, not per recovery point: volume archives are
// written unencrypted even when the dumps are encrypted, because the volume tool
// cannot encrypt. Keying off the set-level flag would hand a passphrase to a
// tool that has no use for it.
func (s *Service) restoreVolume(ctx context.Context, opts Options, volumePrefix string, a dr.Artifact) error {
	name, file := a.Volume, a.File
	if _, err := s.dc.CreateVolume(ctx, name, map[string]string{docker.LabelManaged: "true"}, 0); err != nil {
		return fmt.Errorf("create volume: %w", err)
	}
	env := backup.S3Env(s3Config(opts.S3))
	if a.Encrypted || strings.HasSuffix(file, ".gpg") {
		env = append(env, "GPG_PASSPHRASE="+opts.Passphrase)
	}
	if err := s.dc.PullImage(ctx, s.volImage, nil); err != nil {
		return fmt.Errorf("pull %s: %w", s.volImage, err)
	}
	code, out, err := s.dc.RunOneShot(ctx, docker.RunSpec{
		Name:   "mb-dr-volrestore-" + sanitize(name),
		Image:  s.volImage,
		Env:    env,
		Cmd:    []string{"restore", "--storage", "s3", "--remote-path", volumePrefix, "--file", file},
		Mounts: map[string]string{name: volumeMount},
		Labels: map[string]string{docker.LabelManaged: "true"},
	})
	if err != nil || code != 0 {
		return fmt.Errorf("volume restore exited %d: %s", code, out)
	}
	return nil
}

func s3Config(c blob.Config) *backup.S3Config {
	return &backup.S3Config{
		Endpoint:       c.Endpoint,
		Bucket:         c.Bucket,
		Region:         c.Region,
		AccessKey:      c.AccessKey,
		SecretKey:      c.SecretKey,
		UseSSL:         c.UseSSL,
		ForcePathStyle: c.ForcePathStyle,
	}
}

func manifestPathOf(opts Options) string {
	if p := strings.TrimSpace(opts.ManifestPath); p != "" {
		return p
	}
	return platformstack.ManifestPath()
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// newestRef lists the bucket's identity envelopes and picks the most recent, so
// an operator who lost everything can recover without having memorised a ref.
func (s *Service) newestRef(ctx context.Context, store *blob.Store, prefix string) (string, error) {
	objs, err := store.List(ctx, strings.Trim(prefix, "/"))
	if err != nil {
		return "", err
	}
	var (
		best   string
		bestAt time.Time
	)
	for _, o := range objs {
		ref := dr.RefFromIdentityObject(o.Key)
		if ref == "" {
			continue
		}
		if best == "" || o.UpdatedAt.After(bestAt) {
			best, bestAt = ref, o.UpdatedAt
		}
	}
	if best == "" {
		return "", fmt.Errorf("no recovery points found under %q — check --path, or pass --ref explicitly", prefix)
	}
	return best, nil
}

// ListRefs reports the recovery points discoverable in the bucket, newest first.
// An operator who has lost the platform has also lost the UI that listed them.
func (s *Service) ListRefs(ctx context.Context, cfg blob.Config, prefix string) ([]RefSummary, error) {
	store, err := blob.New(cfg)
	if err != nil {
		return nil, err
	}
	objs, err := store.List(ctx, strings.Trim(prefix, "/"))
	if err != nil {
		return nil, err
	}
	out := make([]RefSummary, 0, 8)
	for _, o := range objs {
		ref := dr.RefFromManifestObject(o.Key)
		if ref == "" {
			continue
		}
		sum := RefSummary{Ref: ref, CreatedAt: o.UpdatedAt}
		// The manifest is small and non-secret, so read it for the detail that
		// makes the list useful rather than just a wall of refs.
		if body, err := store.GetBytes(ctx, o.Key); err == nil {
			if man, err := dr.DecodeManifest(body); err == nil { //nolint:govet // shadow is intentional
				sum.InstallID, sum.MiabiVersion = man.InstallID, man.MiabiVersion
				sum.Encrypted, sum.IdentitySealed = man.Encrypted, man.IdentitySealed
				sum.Artifacts = len(man.Artifacts)
				sum.CreatedAt = man.CreatedAt
			}
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// RefSummary is one discoverable recovery point.
type RefSummary struct {
	Ref            string    `json:"ref"`
	InstallID      string    `json:"install_id,omitempty"`
	MiabiVersion   string    `json:"miabi_version,omitempty"`
	Artifacts      int       `json:"artifacts"`
	Encrypted      bool      `json:"encrypted"`
	IdentitySealed bool      `json:"identity_sealed"`
	CreatedAt      time.Time `json:"created_at"`
}
