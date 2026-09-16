// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stackcmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/pkg/stack"
)

// EnvOptions is the flag surface of the env verbs, already parsed.
type EnvOptions struct {
	// Gateway edits the gateway's environment instead of the control plane's.
	Gateway bool
	// NoApply saves the manifest without converging, for batching several edits.
	NoApply bool
	Yes     bool
}

// EnvList returns the environment the manifest carries, and the name of the block it came from.
func EnvList(path string, o EnvOptions) (map[string]string, string, error) {
	m, err := stack.Load(path)
	if err != nil {
		return nil, "", WithInstallHint(err)
	}
	return envOf(m, o.Gateway), envBlock(o.Gateway), nil
}

// EnvGet returns one variable's value. found is false when the manifest does not carry it, which the
// caller should report as such rather than printing an empty line.
func EnvGet(path, key string, o EnvOptions) (value string, found bool, err error) {
	m, lerr := stack.Load(path)
	if lerr != nil {
		return "", false, WithInstallHint(lerr)
	}
	v, ok := envOf(m, o.Gateway)[strings.TrimSpace(key)]
	return v, ok, nil
}

// EnvSet writes KEY=VALUE pairs into the manifest and converges. Validation is Normalize's: invalid
// names, variables Miabi sets itself, and settings that belong to their own spec section are all
// refused there, with an error that names where the value really lives.
func EnvSet(ctx context.Context, svc *stack.Service, path string, assignments []string, o EnvOptions, ui UI) error {
	if len(assignments) == 0 {
		return errors.New("nothing to set (pass KEY=VALUE)")
	}
	return editEnv(ctx, svc, path, o, ui, nil, func(env map[string]string) error {
		for _, a := range assignments {
			key, value, ok := strings.Cut(a, "=")
			if !ok {
				return fmt.Errorf("%q is not KEY=VALUE", a)
			}
			if key = strings.TrimSpace(key); key == "" {
				return fmt.Errorf("%q has an empty variable name", a)
			}
			env[key] = value
		}
		return nil
	})
}

// EnvUnset removes variables. A key Miabi seeds into the manifest comes back at its default rather
// than disappearing, and the caller is told so — otherwise the operator diffs the file and finds the
// variable still there.
func EnvUnset(ctx context.Context, svc *stack.Service, path string, keys []string, o EnvOptions, ui UI) error {
	if len(keys) == 0 {
		return errors.New("nothing to unset (pass a variable name)")
	}
	clean := make([]string, 0, len(keys))
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			clean = append(clean, k)
		}
	}
	return editEnv(ctx, svc, path, o, ui, clean, func(env map[string]string) error {
		for _, k := range clean {
			delete(env, k)
		}
		return nil
	})
}

// editEnv is the shared body: load, mutate, normalize (which validates), show what changes, save, and
// converge. Saving before converging matches the rest of stackcmd — the file must describe what was
// asked for even if bringing the stack to it fails.
func editEnv(ctx context.Context, svc *stack.Service, path string, o EnvOptions, ui UI, removed []string, mutate func(map[string]string) error) error {
	m, err := stack.Load(path)
	if err != nil {
		return WithInstallHint(err)
	}

	before := cloneEnv(envOf(m, o.Gateway))
	work := cloneEnv(before)
	if err := mutate(work); err != nil {
		return err
	}
	setEnv(m, o.Gateway, work)

	if err := m.Normalize(); err != nil {
		return err
	}
	after := envOf(m, o.Gateway)

	changes := diffEnv(before, after)
	if len(changes) == 0 {
		ui.Info("Nothing to change.")
		reportReseeded(ui, removed, after)
		return nil
	}

	ui.Printf("\n%s:\n\n", envBlock(o.Gateway))
	for _, c := range changes {
		switch {
		case c.from == "":
			ui.Printf("  + %s=%s\n", c.key, c.to)
		case c.to == "":
			ui.Printf("  - %s\n", c.key)
		default:
			ui.Printf("  ~ %s: %s → %s\n", c.key, c.from, c.to)
		}
	}
	ui.Printf("\n")
	reportReseeded(ui, removed, after)

	if !o.Yes && !ui.Confirm("Apply?") {
		return errors.New("cancelled")
	}
	if err := stack.Save(path, m); err != nil {
		return err
	}
	if o.NoApply {
		ui.Warn("Saved, not applied — the running stack still has the old values. `miabi setup` converges it.")
		return nil
	}
	if err := svc.Converge(ctx, m); err != nil {
		return err
	}
	return stack.Save(path, m)
}

// reportReseeded explains a variable that is still present after being unset: Normalize seeds a small
// number of them, so unsetting one restores its default rather than removing it.
func reportReseeded(ui UI, removed []string, after map[string]string) {
	for _, k := range removed {
		if v, still := after[k]; still {
			ui.Info("%s is seeded by Miabi — unsetting it restored the default (%s).", k, v)
		}
	}
}

type envChange struct{ key, from, to string }

func diffEnv(before, after map[string]string) []envChange {
	seen := map[string]bool{}
	var keys []string
	for k := range before {
		keys, seen[k] = append(keys, k), true
	}
	for k := range after {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var out []envChange
	for _, k := range keys {
		if before[k] != after[k] {
			out = append(out, envChange{key: k, from: before[k], to: after[k]})
		}
	}
	return out
}

func envOf(m *stack.Manifest, gateway bool) map[string]string {
	if gateway {
		return m.Gateway.Env
	}
	return m.Env
}

func setEnv(m *stack.Manifest, gateway bool, env map[string]string) {
	if gateway {
		m.Gateway.Env = env
		return
	}
	m.Env = env
}

func envBlock(gateway bool) string {
	if gateway {
		return "spec.gateway.env"
	}
	return "spec.server.env"
}

func cloneEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
