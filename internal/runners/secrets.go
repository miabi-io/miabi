// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/miabi-io/runner/proto"
)

// SecretResolver substitutes ${{ secrets.NAME }} across env values for a
// workspace, erroring on an unknown reference. Implemented by secret.Service.
type SecretResolver interface {
	ResolveAll(workspaceID uint, env []string) ([]string, error)
}

// secretRef matches the reference form used across the platform.
var secretRef = regexp.MustCompile(`\$\{\{\s*secrets\.[A-Za-z][A-Za-z0-9_-]{0,62}\s*\}\}`)

// ErrSecretsUnavailable is returned when a pipeline references a secret but no
// vault is wired. Failing beats sending the literal reference to a runner.
var ErrSecretsUnavailable = errors.New("this pipeline references workspace secrets, but the secret vault is not available")

// resolveJobSecrets replaces secret references throughout a job's env and its
// steps', returning the values to redact from the live log stream. A missing
// secret fails the run here rather than after a build has burned its minutes.
func resolveJobSecrets(r SecretResolver, workspaceID uint, spec *proto.JobSpec) ([]string, error) {
	if !specHasSecretRef(spec) {
		return nil, nil
	}
	if r == nil {
		return nil, ErrSecretsUnavailable
	}

	var mask []string
	resolved, err := r.ResolveAll(workspaceID, spec.Env)
	if err != nil {
		return nil, fmt.Errorf("job env: %w", err)
	}
	mask = append(mask, changedValues(spec.Env, resolved)...)
	spec.Env = resolved

	for i := range spec.Steps {
		step := &spec.Steps[i]
		if len(step.Env) == 0 {
			continue
		}
		out, err := r.ResolveAll(workspaceID, step.Env)
		if err != nil {
			return nil, fmt.Errorf("step %q env: %w", step.Name, err)
		}
		mask = append(mask, changedValues(step.Env, out)...)
		step.Env = out
	}
	return mask, nil
}

func specHasSecretRef(spec *proto.JobSpec) bool {
	if hasSecretRef(spec.Env) {
		return true
	}
	for i := range spec.Steps {
		if hasSecretRef(spec.Steps[i].Env) {
			return true
		}
	}
	return false
}

func hasSecretRef(env []string) bool {
	for _, e := range env {
		if secretRef.MatchString(e) {
			return true
		}
	}
	return false
}

// changedValues returns the resolved value of every entry resolution rewrote.
// Whatever a reference expanded to is secret by definition, so this is the set
// the log stream must mask.
func changedValues(before, after []string) []string {
	var out []string
	for i := range after {
		if i >= len(before) || after[i] == before[i] {
			continue
		}
		if v := valueOf(after[i]); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// valueOf returns the value half of a KEY=VALUE entry.
func valueOf(entry string) string {
	for i := 0; i < len(entry); i++ {
		if entry[i] == '=' {
			return entry[i+1:]
		}
	}
	return ""
}
