// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/enterprise/license"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "keygen":
		keygen()
	case "flags":
		listFlags()
	case "tiers":
		listTiers()
	case "issue":
		issue(os.Args[2:])
	case "verify":
		verify(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: miabi-license <keygen|flags|tiers|issue|verify> [flags]")
}

func listTiers() {
	fmt.Println("commercial tiers (use with `issue --tier <name>`):")
	for _, t := range enterprise.Tiers {
		fmt.Printf("\n  %-14s %s\n", t.Name, t.Desc)
		fmt.Printf("    node_limit=%d plan_limit=%d\n", t.Limits[enterprise.LimitNodeLimit], t.Limits[enterprise.LimitPlanLimit])
		fmt.Printf("    flags: %s\n", strings.Join(t.Flags, ", "))
	}
}

func listFlags() {
	fmt.Println("entitlement flags (use with `issue --flags a,b,c` or grant all with `--all-flags`):")
	for _, f := range enterprise.AllFlags {
		fmt.Printf("  %-22s %s\n", f.Name, f.Desc)
	}
}

func keygen() {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		fail(err)
	}
	fmt.Printf("public_key:  %s\n", pub)
	fmt.Printf("private_key: %s\n", priv)
	fmt.Fprintln(os.Stderr, "\nBake public_key into the server (MIABI_LICENSE_PUBLIC_KEY or -ldflags). Keep private_key secret.")
}

func issue(args []string) {
	fs := flag.NewFlagSet("issue", flag.ExitOnError)
	var (
		licenseID = fs.String("id", "", "license id (default: lic_<unix>)")
		customer  = fs.String("customer", "", "customer name")
		edition   = fs.String("edition", "enterprise", "edition: enterprise")
		tier      = fs.String("tier", "", "plan preset: professional|business|enterprise (expands flags+limits; run `tiers`)")
		installID = fs.String("install-id", "", "bind the license to one instance by its Install ID (the customer's 'Your Install ID'); the strong, primary binding")
		urlBind   = fs.String("url", "", "also bind by deployment URL (matched by host); empty = no URL binding")
		flags     = fs.String("flags", "", "comma-separated entitlement flags (unioned onto the tier)")
		nodeLimit = fs.Int("node-limit", -1, "max active nodes (-1 = unlimited); overrides the tier")
		planLimit = fs.Int("plan-limit", -1, "max platform plans (-1 = unlimited); overrides the tier")
		validDays = fs.Int("valid-days", 365, "validity window in days")
		graceDays = fs.Int("grace-days", 14, "grace days after expiry")
		notBefore = fs.String("not-before", "", "RFC3339 start (default: now)")
		allFlags  = fs.Bool("all-flags", false, "grant every entitlement flag (overrides --flags/--tier)")
		key       = fs.String("key", "", "base64 Ed25519 private signing key")
		out       = fs.String("out", "", "write the token to this file instead of stdout")
	)
	_ = fs.Parse(args)
	// Track which flags were explicitly passed so a tier preset's values aren't
	// clobbered by defaults (e.g. the -1 default of --node-limit).
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	signKey := firstNonEmpty(*key, os.Getenv("MIABI_LICENSE_SIGNING_KEY"))
	if signKey == "" {
		fail(fmt.Errorf("a signing key is required (--key or MIABI_LICENSE_SIGNING_KEY)"))
	}
	if *customer == "" {
		fail(fmt.Errorf("--customer is required"))
	}

	// A tier preset supplies the base flags and limits; explicit flags layer on top.
	limits := map[string]int{enterprise.LimitNodeLimit: *nodeLimit, enterprise.LimitPlanLimit: *planLimit}
	var entFlags []string
	tierName := ""
	if *tier != "" {
		preset, ok := enterprise.TierByName(*tier)
		if !ok {
			fail(fmt.Errorf("unknown --tier %q (run `miabi-license tiers` for the valid set)", *tier))
		}
		tierName = preset.Name
		entFlags = append(entFlags, preset.Flags...)
		limits = map[string]int{}
		for k, v := range preset.Limits {
			limits[k] = v
		}
		if set["node-limit"] {
			limits[enterprise.LimitNodeLimit] = *nodeLimit
		}
		if set["plan-limit"] {
			limits[enterprise.LimitPlanLimit] = *planLimit
		}
	}
	entFlags = append(entFlags, splitFlags(*flags)...)
	if *allFlags {
		entFlags = enterprise.FlagNames()
	}
	entFlags = dedupeFlags(entFlags)
	// Reject unknown flags up front — a typo'd flag would silently grant nothing
	// and produce a license that looks complete but isn't.
	for _, f := range entFlags {
		if !enterprise.IsKnownFlag(f) {
			fail(fmt.Errorf("unknown entitlement flag %q (run `miabi-license flags` for the valid set)", f))
		}
	}

	start := time.Now().UTC()
	if *notBefore != "" {
		t, err := time.Parse(time.RFC3339, *notBefore)
		if err != nil {
			fail(fmt.Errorf("invalid --not-before: %w", err))
		}
		start = t
	}
	id := *licenseID
	if id == "" {
		id = fmt.Sprintf("lic_%d", start.Unix())
	}

	c := license.Claims{
		LicenseID: id,
		Customer:  *customer,
		Edition:   *edition,
		Tier:      tierName,
		InstallID: strings.TrimSpace(*installID),
		URL:       strings.TrimSpace(*urlBind),
		Flags:     entFlags,
		Limits:    limits,
		NotBefore: start,
		NotAfter:  start.AddDate(0, 0, *validDays),
		GraceDays: *graceDays,
		IssuedAt:  start,
	}
	tok, err := license.Sign(signKey, c)
	if err != nil {
		fail(err)
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(tok+"\n"), 0o600); err != nil {
			fail(err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (id=%s customer=%q edition=%s tier=%q install_id=%q url=%q flags=%d)\n", *out, id, *customer, *edition, tierName, c.InstallID, c.URL, len(entFlags))
		return
	}
	fmt.Println(tok)
}

func verify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	key := fs.String("key", "", "base64 Ed25519 public key")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 1 {
		fail(fmt.Errorf("usage: miabi-license verify --key <pub> <file>"))
	}
	pub := firstNonEmpty(*key, os.Getenv("MIABI_LICENSE_PUBLIC_KEY"))
	if pub == "" {
		fail(fmt.Errorf("a public key is required (--key or MIABI_LICENSE_PUBLIC_KEY)"))
	}
	raw, err := os.ReadFile(rest[0])
	if err != nil {
		fail(err)
	}
	c, err := license.Verify(pub, strings.TrimSpace(string(raw)))
	if err != nil {
		fail(err)
	}
	s := license.Evaluate(c, time.Now())
	installID := c.InstallID
	if installID == "" {
		installID = "(any instance)"
	}
	url := c.URL
	if url == "" {
		url = "(any URL)"
	}
	binding := "unlimited (any instance, any number)"
	if c.InstallID != "" || c.URL != "" {
		binding = "bound"
	}
	fmt.Printf("valid signature ✓\n  license_id: %s\n  customer:   %s\n  edition:    %s\n  tier:       %s\n  binding:    %s\n  install_id: %s\n  url:        %s\n  flags:      %v\n  limits:     %v\n  not_after:  %s\n  state:      %s\n",
		c.LicenseID, c.Customer, c.Edition, c.Tier, binding, installID, url, c.Flags, c.Limits, c.NotAfter.Format(time.RFC3339), s.State)
}

// dedupeFlags preserves first-seen order; a tier preset and explicit --flags may overlap.
func dedupeFlags(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, f := range in {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func splitFlags(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
