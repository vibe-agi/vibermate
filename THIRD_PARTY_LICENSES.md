# Third-party licenses

## Adapted Go JSON scanner

The private JSON syntax scanner in
`internal/runtimepersistence/exchange_content_json_scan.go` is adapted from
Go 1.26.8, `src/encoding/json/scanner.go` (Copyright 2010 The Go Authors).
Upstream SHA256: `2b16dd215274dfa8e0806b53cca144684d5b9c8a02319cf5ce10120c50e6eef2`.
Local changes remove the pool, convenience API and build tag, privatize types,
reset byte offsets, and reserve a caller-owned bounded nesting stack before
growth. The grammar state transitions are retained. This is adapted source,
not an additional module dependency. The complete upstream license follows
and is also retained as `internal/runtimepersistence/GO_JSON_SCANNER_LICENSE`.

Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

This inventory records the important direct dependencies and bundled assets
observed in the current source tree and lock files. It is a practical notice,
not a substitute for a release-specific SBOM or legal review. Release
artifacts should continue to carry the dependency notices required by each
dependency.

## Compatibility legend

- Permissive: generally compatible with distributing a larger work under
  AGPLv3 or a separate Commercial License, provided notices and disclaimers
  are retained.
- File-level copyleft: compatible with a larger distribution, but modified
  files remain subject to their own license and cannot simply be relicensed as
  proprietary.
- Development-only: used by tests or tooling and not intended to ship in the
  runtime artifact.
- “Compatible” here means no obvious license conflict was found in this
  limited review; it is not a legal opinion.

## Go runtime dependencies

| Dependency | Version | License observed | Commercial License compatibility | Notes |
| --- | --- | --- | --- | --- |
| github.com/anthropics/anthropic-sdk-go | v1.61.0 | MIT | Permissive | Retain MIT notice. |
| github.com/caddyserver/certmagic | v0.25.4 | Apache-2.0 | Permissive | Retain Apache notice and any bundled notices. |
| github.com/dop251/goja | pinned 2026 revision | MIT | Permissive | JavaScript runtime; retain MIT notice. |
| github.com/hashicorp/golang-lru/v2 | v2.0.7 | MPL-2.0 | File-level copyleft | Modified MPL files remain MPL-2.0; do not relicense those files. |
| github.com/klauspost/compress | v1.19.1 | BSD-3-Clause and Apache-2.0 files | Permissive | The module contains per-file notices; retain them when distributing. |
| github.com/openai/openai-go/v3 | v3.46.0 | Apache-2.0 | Permissive | Retain Apache notice. |
| github.com/refraction-networking/utls | v1.8.2 | BSD-3-Clause | Permissive | Includes Go Authors notice. |
| go.uber.org/zap | v1.27.1 | MIT | Permissive | Retain MIT notice. |
| golang.org/x/crypto, x/net, x/sync, x/sys, x/term | pinned | BSD-3-Clause | Permissive | Go project licenses and notices. |
| modernc.org/sqlite | v1.54.0 | BSD-3-Clause | Permissive | SQLite and modernc notices still apply. |

The Go module graph also includes Let’s Encrypt Pebble and challtestsrv under
MPL-2.0 for HTTPS/ACME tests. They are development-only and are not part of
the normal Runtime distribution. Other indirect modules are covered by the
release SBOM and their upstream notices.

The main Go dependency review found no direct runtime dependency under GPL,
AGPL, SSPL, BSL, Elastic License, Commons Clause, an explicit
non-commercial license, or another source-available restriction. This does
not clear every transitive package for every distribution; regenerate and
review the release SBOM before a Commercial License delivery.

## Flutter/Dart dependencies

| Dependency | Version | License observed | Commercial License compatibility | Notes |
| --- | --- | --- | --- | --- |
| crypto | 3.0.7 lock | BSD-3-Clause | Permissive | Dart project authors notice. |
| cupertino_icons | 1.0.9 | MIT | Permissive | Retain MIT notice. |
| flutter_markdown_plus | 1.0.12 | BSD-3-Clause | Permissive | Flutter Authors notice. |
| flutter_svg | 2.3.0 | MIT | Permissive | Retain MIT notice. |
| file_selector | 1.1.0 | BSD-3-Clause | Permissive | Flutter Authors notice. |
| http | 1.6.0 | BSD-3-Clause | Permissive | Dart project authors notice. |
| markdown | 7.3.1 | BSD-3-Clause | Permissive | Dart project authors notice. |
| url_launcher | 6.3.2 | BSD-3-Clause | Permissive | Flutter Authors notice. |
| web | 1.1.1 | BSD-3-Clause | Permissive | Dart project authors notice. |
| flutter_lints | 6.0.0 | BSD-3-Clause | Development-only | Lint rules, not a runtime dependency. |
| file_selector_platform_interface | 2.7.0 | BSD-3-Clause | Permissive | Retain Flutter notice if bundled. |
| Flutter SDK | 3.41.5 (pinned toolchain) | BSD-3-Clause | Permissive | The SDK and bundled platform material retain Flutter notices. |
| CanvasKit (Skia) | engine of the pinned Flutter SDK | BSD-3-Clause | Permissive | Bundled in the Web workbench instead of loading from a CDN. |
| Noto and Roboto fonts | pinned in ui/flutter_app/tool/web_fallback_fonts.json | SIL OFL 1.1 | Permissive | Unmodified Web fallback fonts; the license travels as vibermate-web/fonts/LICENSE.txt. |

The bundled agent icons are from Lobe Icons and are separately licensed under
the MIT License; their notice is in
ui/flutter_app/assets/agent-icons/LICENSE.txt.

## What this means for dual licensing

The reviewed dependencies do not obviously prevent the project maintainer
from licensing the maintainer-owned ViberMate code under both AGPLv3 and a
separate Commercial License. They do not give the maintainer ownership of
third-party code: a Commercial License distribution must continue to respect
the dependency licenses, notices, patent terms, and any file-level copyleft
requirements.

Before shipping a closed-source product that embeds ViberMate, review the
exact dependency graph, generated assets, platform plugins, container
packages, and any customer-specific additions with qualified counsel.
