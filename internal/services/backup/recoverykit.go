// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"fmt"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/dbenvelope"
	"github.com/miabi-io/miabi/internal/models"
)

// RecoveryKit renders everything needed to read a recovery point back without
// Miabi: where the artifacts are, the sealed envelope, the exact envelope format,
// and the commands that get from a passphrase to a restorable dump.
//
// The kit deliberately does NOT contain the data key — only the envelope it is
// sealed in. A kit is therefore useless without the passphrase, which is the
// property that makes it safe to keep beside the backups it describes.
func (s *Service) RecoveryKit(set *models.DatabaseBackupSet) (string, []byte, error) {
	if set == nil {
		return "", nil, fmt.Errorf("no recovery point")
	}
	filename := set.Ref + "-recovery-kit.md"
	return filename, []byte(renderRecoveryKit(set, time.Now().UTC())), nil
}

func renderRecoveryKit(set *models.DatabaseBackupSet, at time.Time) string {
	var b strings.Builder
	f := dbenvelope.Format()

	fmt.Fprintf(&b, "# Miabi recovery kit — %s\n\n", set.Ref)
	fmt.Fprintf(&b, "Generated %s\n\n", at.Format(time.RFC3339))
	b.WriteString("Keep this with your break-glass credentials. It describes how to read this\n")
	b.WriteString("recovery point back **without Miabi running**, using only the backup passphrase.\n\n")

	b.WriteString("## What this covers\n\n")
	fmt.Fprintf(&b, "- Recovery point: `%s`\n", set.Ref)
	fmt.Fprintf(&b, "- Engine: %s %s\n", set.Engine, set.Version)
	fmt.Fprintf(&b, "- Taken: %s\n", set.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Encrypted: %t\n", set.Encrypted)
	if set.S3Bucket != "" {
		fmt.Fprintf(&b, "- Bucket: `%s`\n", set.S3Bucket)
		fmt.Fprintf(&b, "- Prefix: `%s`\n", set.S3Path)
	}
	b.WriteString("\n### Artifacts\n\n")
	if len(set.Items) == 0 {
		b.WriteString("_This recovery point recorded no artifacts._\n\n")
	} else {
		for i := range set.Items {
			it := &set.Items[i]
			name := it.Filename
			if name == "" {
				name = "(no artifact — this item did not complete)"
			}
			fmt.Fprintf(&b, "- `%s`\n", name)
		}
		b.WriteString("\n")
	}

	if set.Envelope == "" {
		b.WriteString("## Not encrypted\n\n")
		b.WriteString("This recovery point was taken without a backup passphrase, so its artifacts\n")
		b.WriteString("are stored as written by the backup tool. Download them from the bucket and\n")
		b.WriteString("restore with the engine's own tooling — no key material is involved.\n")
		return b.String()
	}

	b.WriteString("## The sealed envelope\n\n")
	b.WriteString("This is the data key for this recovery point, sealed under your backup\n")
	b.WriteString("passphrase. It is useless without that passphrase, and the passphrase is not\n")
	b.WriteString("recorded anywhere in this file.\n\n")
	b.WriteString("```\n")
	b.WriteString(set.Envelope)
	b.WriteString("\n```\n\n")

	b.WriteString("## Envelope format\n\n")
	b.WriteString("The decoded envelope is:\n\n")
	b.WriteString("```\n")
	fmt.Fprintf(&b, "%-16s %d bytes  ASCII %q\n", "magic", len(f.Magic), f.Magic)
	fmt.Fprintf(&b, "%-16s 1 byte   format version %d\n", "version", f.FormatVersion)
	fmt.Fprintf(&b, "%-16s %d bytes\n", "salt", f.SaltLen)
	fmt.Fprintf(&b, "%-16s %d bytes\n", "nonce", f.NonceLen)
	fmt.Fprintf(&b, "%-16s rest     %s\n", "ciphertext", f.Cipher)
	b.WriteString("```\n\n")
	fmt.Fprintf(&b, "The key is %s with time=%d, memory=%d KiB, lanes=%d, producing %d bytes.\n",
		f.KDF, f.ArgonTime, f.ArgonMemoryKB, f.ArgonLanes, f.KeyLen)
	fmt.Fprintf(&b, "The first %d bytes (magic through nonce) are authenticated as additional data,\n", f.HeaderLen)
	b.WriteString("so an edited header fails to open rather than decrypting to nonsense.\n\n")

	b.WriteString("## Opening it\n\n")
	b.WriteString("Needs `pip install argon2-cffi cryptography`. Save this as `open.py`, then\n")
	b.WriteString("`python open.py envelope.txt` prints the data key:\n\n")
	b.WriteString("```python\n")
	b.WriteString(kitPythonScript(f))
	b.WriteString("```\n\n")

	b.WriteString("## Decrypting a dump\n\n")
	b.WriteString("The data key printed above is the GPG passphrase for every artifact in this\n")
	b.WriteString("recovery point:\n\n")
	b.WriteString("```sh\n")
	example := "<artifact>.gpg"
	if len(set.Items) > 0 && set.Items[0].Filename != "" {
		example = set.Items[0].Filename
	}
	fmt.Fprintf(&b, "gpg --batch --yes --passphrase \"$DATA_KEY\" --decrypt %s > %s\n",
		example, strings.TrimSuffix(example, ".gpg"))
	b.WriteString("```\n\n")
	b.WriteString("That leaves the dump exactly as the backup tool wrote it — a gzipped SQL dump\n")
	b.WriteString("or Mongo archive — which restores with the engine's own tooling.\n")
	return b.String()
}

// kitPythonScript is a runnable reimplementation of Open, so the kit does not
// depend on Miabi being installed to be useful.
func kitPythonScript(f dbenvelope.Spec) string {
	return fmt.Sprintf(`import base64, getpass, sys
from argon2.low_level import hash_secret_raw, Type
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

# Usage: python open.py envelope.txt   (or pipe the envelope on stdin)
# Prompts on stderr and prints ONLY the data key, so the output can be redirected.
MAGIC, SALT, NONCE, HEADER = b%q, %d, %d, %d

src = open(sys.argv[1]) if len(sys.argv) > 1 else sys.stdin
envelope = base64.b64decode(src.read().strip())
if envelope[:len(MAGIC)] != MAGIC:
    sys.exit("not a Miabi backup envelope")
if envelope[len(MAGIC)] != %d:
    sys.exit("unsupported envelope format version")

salt  = envelope[len(MAGIC)+1 : len(MAGIC)+1+SALT]
nonce = envelope[len(MAGIC)+1+SALT : HEADER]
key = hash_secret_raw(
    secret=getpass.getpass("backup passphrase: ", stream=sys.stderr).encode(),
    salt=salt, time_cost=%d, memory_cost=%d, parallelism=%d,
    hash_len=%d, type=Type.ID,
)
sys.stdout.write(AESGCM(key).decrypt(nonce, envelope[HEADER:], envelope[:HEADER]).decode())
`, f.Magic, f.SaltLen, f.NonceLen, f.HeaderLen, f.FormatVersion,
		f.ArgonTime, f.ArgonMemoryKB, f.ArgonLanes, f.KeyLen)
}
