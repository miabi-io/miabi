// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package dr

import (
	"fmt"
	"time"
)

// ExampleEncodeManifest shows the info file an operator finds in the bucket: XML,
// readable, and stating in the file itself why it is not encrypted.
func ExampleEncodeManifest() {
	ref := NewRef("mbi_demo", time.Unix(1_760_000_000, 0).UTC())
	m := &Manifest{
		Schema: ManifestSchema, Ref: ref,
		InstallID: "mbi_demo", MiabiVersion: "1.7.3", KEKFingerprint: "mrt_abc",
		Encrypted: true, IdentitySealed: true, Prefix: "prod/databases",
		Artifacts: []Artifact{{Subject: SubjectDatabase, File: "miabi_2026.sql.gz.gpg", Encrypted: true}},
		CreatedAt: time.Unix(1_760_000_000, 0).UTC(),
	}
	body, err := EncodeManifest(m)
	if err != nil {
		panic(err)
	}
	fmt.Println(ManifestObject("prod", ref))
	fmt.Println(string(body)[:38])
	// Output:
	// prod/recovery-mbdr_mbi_demo_20251009T085320Z.xml
	// <?xml version="1.0" encoding="UTF-8"?>
}
