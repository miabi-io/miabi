# Licensing

Miabi is **open-core**, split across three licenses.

## Miabi Community — AGPL-3.0-or-later

Miabi core is free and open source under the **GNU Affero General Public License,
version 3 or (at your option) any later version (AGPL-3.0-or-later)**. See
[LICENSE](./LICENSE) and [NOTICE](./NOTICE). This covers the entire codebase
except the reusable packages and the Enterprise Edition files described below —
including the Community build (everything compiled into the default, tag-free
binary).

The AGPL is a strong copyleft license with a **network clause** (section 13): if
you run a modified version of Miabi and let users interact with it over a network
(e.g. you host it as a service), you must offer those users the **complete
corresponding source** of your modified version, also under the AGPL. Running
Miabi unmodified, or for your own internal use, imposes no such obligation beyond
the usual AGPL terms.

If the AGPL's obligations don't fit your use — for example you want to offer a
modified Miabi as a hosted service without publishing your changes, or embed it in
a proprietary product — a **commercial license is available** (see below).

## Reusable packages (`pkg/`) — Apache-2.0

Everything under [`pkg/`](./pkg) is licensed under the **Apache License, Version
2.0**. See [`pkg/LICENSE`](./pkg/LICENSE).

These are the packages meant to be imported by other programs. `pkg/stack` is the
installer and lifecycle engine for a Miabi host: it is what the standalone
[miabi CLI](https://github.com/miabi-io/cli) drives when it installs, upgrades or
tears down a stack, and the CLI is distributed separately from the control plane.
A permissive license here means that CLI — and any third-party tool that wants to
provision Miabi — can embed the engine without taking on the AGPL's obligations.

The boundary is enforced by direction, not by convention: **nothing under `pkg/`
imports `internal/`**, so the Apache-licensed code never links AGPL code. The
reverse is fine and is what the control plane does — AGPL code may use Apache
code. The one test that spans both lives on the AGPL side, in
`internal/config/loglevel_agreement_test.go`.

`pkg/stack/assets/goma.yml` is embedded in the package and is therefore covered by
Apache-2.0 there. The identical file at `examples/compose/goma.yml` is part of the
AGPL tree; a test asserts the two stay byte-identical. Both copies are the
copyright holder's to license, and each is offered under the license of the tree it
sits in.

## Miabi Enterprise — Commercial License

Enterprise features are available under a commercial **Miabi Enterprise License**
(Enterprise Edition). The Enterprise files are the sources under
[`internal/enterprise/`](./internal/enterprise) that are built with the
`enterprise` build tag. They are **not** AGPL and require a valid commercial
license to use; the full terms are defined in
[`internal/enterprise/LICENSE.md`](./internal/enterprise/LICENSE.md).

## Dual licensing & the AGPL + Enterprise combination

Jonas Kaninda holds the copyright to the Miabi core, and therefore offers it under
**both** the AGPL (to everyone) and, separately, a **commercial license** to those
who cannot or do not wish to comply with the AGPL. The copyright holder is not
bound by the AGPL for its own code, so it may combine the AGPL-licensed core with
the proprietary Enterprise Edition in a single binary and license that combined
work commercially. This is the basis on which the `enterprise`-tagged build is
distributed.

Contributions to the AGPL core are accepted under a Contributor License Agreement
(see [CONTRIBUTING.md](./CONTRIBUTING.md)) precisely so this dual-licensing —
AGPL to the public, plus a proprietary Enterprise Edition — remains lawful as the
project grows beyond a single author.

For commercial licensing, contact the Miabi project maintainers.

## Per-file markers (SPDX)

Every source file declares its license inline with an [SPDX](https://spdx.dev)
identifier, so the AGPL / Apache / Enterprise boundaries are explicit and
machine-readable:

- Community core: `SPDX-License-Identifier: AGPL-3.0-or-later`
- Reusable packages under `pkg/`: `SPDX-License-Identifier: Apache-2.0`
- Enterprise: `SPDX-License-Identifier: LicenseRef-Miabi-Enterprise`

`LicenseRef-Miabi-Enterprise` is the custom (non-SPDX-registered) Miabi Enterprise
License whose full text lives in
[`internal/enterprise/LICENSE.md`](./internal/enterprise/LICENSE.md). Only the
files built with the `enterprise` tag carry it (the SAML, LDAP/AD, and SCIM
providers plus the license-verifying `enterprise.go`); everything under `pkg/` is
Apache-2.0, and everything else is AGPL-3.0-or-later. The community stub
(`ce_stub.go`), the shared `EE` interface (`ee.go`), and the always-compiled
license-token verification (`internal/enterprise/license/`) ship in the
open-source build and are AGPL-3.0-or-later.
