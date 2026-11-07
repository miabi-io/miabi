// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package dr

import (
	"fmt"
	"time"
)

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
