// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jkaninda/okapi/okapicli"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/services/platformrestore"
	"github.com/miabi-io/miabi/internal/storage/blob"
)

// registerRestoreCommands adds the disaster-recovery commands.
//
// They live beside `install` rather than behind the admin API for one reason:
// the host they run on has no Miabi. There is no database to authenticate
// against, no settings row holding the bucket credentials, and no admin account
// — the credentials needed to reach the backup are themselves inside the backup.
// So recovery is a host operation, driven by an operator who holds the
// passphrase, exactly like the install it replaces.
func registerRestoreCommands(cli *okapicli.CLI) {
	cli.Command("restore", "Rebuild a Miabi platform on this host from a recovery point", runRestore).
		String("from", "", "", "Backup target, e.g. s3://bucket/prefix").
		String("ref", "", "", "Recovery point to restore (default: the newest one found)").
		String("endpoint", "", "", "S3 endpoint for MinIO and other S3-compatible stores").
		String("region", "", "", "S3 region").
		String("access-key", "", "", "S3 access key (or MIABI_S3_ACCESS_KEY)").
		String("secret-key", "", "", "S3 secret key (or MIABI_S3_SECRET_KEY)").
		Bool("no-ssl", "", false, "Talk to the endpoint over plain HTTP").
		Bool("path-style", "", false, "Use path-style bucket addressing (MinIO and most self-hosted gateways)").
		String("passphrase-file", "", "", "File holding the backup passphrase (or MIABI_BACKUP_PASSPHRASE)").
		String("domain", "d", "", "Serve on this hostname instead of the recovered one").
		Bool("clone", "", false, "Restore as a SEPARATE platform: mints a new install id; requires --domain").
		String("image", "", "", "Control-plane image (default: the image this binary runs from)").
		String("file", "f", "", "Manifest path").
		Bool("dry-run", "", false, "Validate the recovery point and print the plan without changing anything").
		Bool("yes", "y", false, "Do not prompt")

	cli.Command("recovery-points", "List the recovery points in a backup target", runRecoveryPoints).
		String("from", "", "", "Backup target, e.g. s3://bucket/prefix").
		String("endpoint", "", "", "S3 endpoint for MinIO and other S3-compatible stores").
		String("region", "", "", "S3 region").
		String("access-key", "", "", "S3 access key (or MIABI_S3_ACCESS_KEY)").
		String("secret-key", "", "", "S3 secret key (or MIABI_S3_SECRET_KEY)").
		Bool("no-ssl", "", false, "Talk to the endpoint over plain HTTP").
		Bool("path-style", "", false, "Use path-style bucket addressing")
}

func runRestore(cmd *okapicli.Command) error {
	s3cfg, prefix, err := s3From(cmd)
	if err != nil {
		return err
	}
	pass, err := passphraseFrom(cmd)
	if err != nil {
		return err
	}

	svc, dc, err := restoreService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	opts := platformrestore.Options{
		S3:           s3cfg,
		Ref:          strings.TrimSpace(cmd.GetString("ref")),
		Path:         prefix,
		Passphrase:   pass,
		Domain:       strings.TrimSpace(cmd.GetString("domain")),
		Clone:        cmd.GetBool("clone"),
		Image:        strings.TrimSpace(cmd.GetString("image")),
		ManifestPath: manifestPath(cmd),
		DryRun:       cmd.GetBool("dry-run"),
	}
	if opts.Image == "" {
		opts.Image = defaultImage(context.Background(), dc)
	}

	ctx, cancel := stackCtx()
	defer cancel()

	fmt.Println("\nChecking the recovery point…")
	plan, err := svc.Preflight(ctx, opts)
	if err != nil {
		return err
	}
	printPlan(plan, opts)

	if opts.DryRun {
		fmt.Println("\n✓ Dry run: the recovery point is valid and nothing was changed.")
		return nil
	}
	if !cmd.GetBool("yes") && !confirm("Restore this platform onto this host?") {
		return errors.New("cancelled")
	}

	fmt.Println()
	if err := svc.Restore(ctx, plan, opts); err != nil {
		return err
	}

	fmt.Printf("\n✓ Control plane restored from %s\n", plan.Ref)
	fmt.Printf("\n  WHAT IS RUNNING: Miabi itself — its database, its gateway, and the panel.\n" +
		"  WHAT IS NOT: your workspaces. No app containers, no databases, no volumes\n" +
		"  exist on this host yet. The panel will list them all, because the records\n" +
		"  came back; the workloads have not.\n")
	fmt.Printf("\n  NEXT — this is not optional:\n" +
		"    1. Sign in to the panel.\n" +
		"    2. Admin → Platform Backup → \"Reconcile now\". That is what recreates the\n" +
		"       networks, brings the databases back up, restores their data and your\n" +
		"       volumes, and redeploys the apps. Read the report it produces.\n" +
		"    3. Point DNS at this host.\n" +
		"    4. \"Complete recovery\" — schedules and certificate issuance resume.\n")
	fmt.Printf("\n  Until step 2, the platform stays quiesced on purpose: DNS may still point\n" +
		"  at the host you recovered from, and a platform that starts reconciling and\n" +
		"  requesting certificates against it makes the outage worse.\n")
	if plan.NewInstallID != "" {
		fmt.Printf("\n  This is a clone. New install id: %s (it needs its own license).\n", plan.NewInstallID)
	}
	fmt.Println("\n  Still to do:")
	for _, step := range plan.Manual {
		fmt.Printf("    · %s\n", step)
	}
	printManageHint(opts.Image, opts.ManifestPath)
	return nil
}

func runRecoveryPoints(cmd *okapicli.Command) error {
	s3cfg, prefix, err := s3From(cmd)
	if err != nil {
		return err
	}
	svc, dc, err := restoreService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	ctx, cancel := stackCtx()
	defer cancel()
	refs, err := svc.ListRefs(ctx, s3cfg, prefix)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return fmt.Errorf("no recovery points found in %s/%s", s3cfg.Bucket, prefix)
	}
	fmt.Printf("\n%-46s %-12s %-9s %s\n", "REF", "VERSION", "ARTIFACTS", "TAKEN")
	for _, r := range refs {
		sealed := ""
		if !r.IdentitySealed {
			// Worth calling out: without an envelope this cannot rebuild a fresh host.
			sealed = "  (no identity envelope)"
		}
		fmt.Printf("%-46s %-12s %-9d %s%s\n", r.Ref, orDash(r.MiabiVersion), r.Artifacts,
			r.CreatedAt.Format("2006-01-02 15:04 MST"), sealed)
	}
	fmt.Println()
	return nil
}

func printPlan(plan *platformrestore.Plan, opts platformrestore.Options) {
	fmt.Printf("\nMiabi will restore:\n\n")
	fmt.Printf("  recovery point  %s\n", plan.Ref)
	fmt.Printf("  taken by        Miabi %s\n", orDash(plan.MiabiVersion))
	fmt.Printf("  install id      %s\n", plan.InstallID)
	if opts.Clone {
		fmt.Printf("                  (a NEW install id will be minted for this clone)\n")
	}
	fmt.Printf("  domain          %s\n", plan.Domain)
	fmt.Printf("  database        %s\n", plan.DatabaseFile)
	if len(plan.Volumes) > 0 {
		fmt.Printf("  volumes         %s\n", strings.Join(plan.Volumes, ", "))
	}
	fmt.Printf("  encrypted       %v\n", plan.Encrypted)
	fmt.Printf("  manifest        %s\n", opts.ManifestPath)
	fmt.Printf("\n  The encryption key was recovered from the identity envelope and matches this\n" +
		"  recovery point, so the restored secrets will decrypt.\n")
	if len(plan.MissingTenantData) > 0 {
		fmt.Printf("\n  NOT in the bucket — this data will not come back:\n")
		for _, m := range plan.MissingTenantData {
			fmt.Printf("    · %s\n", m)
		}
	}
	if len(plan.Manual) > 0 {
		fmt.Printf("\n  This restore cannot do the following for you:\n")
		for _, step := range plan.Manual {
			fmt.Printf("    · %s\n", step)
		}
	}
}

// restoreService builds the restore service over the host's Docker.
func restoreService(cmd *okapicli.Command) (*platformrestore.Service, docker.Client, error) {
	stack, dc, err := stackService(cmd)
	if err != nil {
		return nil, nil, err
	}
	svc := platformrestore.New(dc, stack, func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) })
	return svc, dc, nil
}

// s3From parses --from plus the credential flags into a blob config and the
// object prefix within the bucket.
func s3From(cmd *okapicli.Command) (blob.Config, string, error) {
	raw := strings.TrimSpace(cmd.GetString("from"))
	if raw == "" {
		return blob.Config{}, "", errors.New("--from is required, e.g. --from s3://my-bucket/miabi/platform")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(raw, "s3://"), "S3://")
	if rest == raw && strings.Contains(raw, "://") {
		return blob.Config{}, "", fmt.Errorf("--from must be an s3:// URL, got %q", raw)
	}
	bucket, prefix, _ := strings.Cut(strings.Trim(rest, "/"), "/")
	if bucket == "" {
		return blob.Config{}, "", errors.New("--from is missing a bucket")
	}

	access := firstNonEmpty(cmd.GetString("access-key"), os.Getenv("MIABI_S3_ACCESS_KEY"))
	secret := firstNonEmpty(cmd.GetString("secret-key"), os.Getenv("MIABI_S3_SECRET_KEY"))
	if access == "" || secret == "" {
		return blob.Config{}, "", errors.New("S3 credentials are required: pass --access-key/--secret-key, or set MIABI_S3_ACCESS_KEY/MIABI_S3_SECRET_KEY")
	}
	return blob.Config{
		Endpoint:       strings.TrimSpace(cmd.GetString("endpoint")),
		Bucket:         bucket,
		Region:         strings.TrimSpace(cmd.GetString("region")),
		AccessKey:      access,
		SecretKey:      secret,
		UseSSL:         !cmd.GetBool("no-ssl"),
		ForcePathStyle: cmd.GetBool("path-style"),
	}, prefix, nil
}

// passphraseFrom reads the backup passphrase from a file or the environment.
//
// Never from a flag: a passphrase on the command line lands in the shell history
// and in every `ps` on the box, and this one opens the platform's master key.
//
// An empty result is not an error here. Whether a passphrase is needed depends on
// the recovery point, and preflight has read it — so it can say "this recovery
// point is sealed and you gave no passphrase" instead of this function guessing
// and reporting a requirement that may not apply.
func passphraseFrom(cmd *okapicli.Command) (string, error) {
	if p := strings.TrimSpace(cmd.GetString("passphrase-file")); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("read passphrase file: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return os.Getenv("MIABI_BACKUP_PASSPHRASE"), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
