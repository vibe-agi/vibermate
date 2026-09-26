# Contributing to ViberMate

Thank you for helping improve ViberMate. Start with a focused issue or pull
request, describe the user-visible problem, and include the smallest
reproducible test or example you can.

## Development

Keep changes scoped and preserve the existing domain boundaries. Before
opening a pull request, run the checks relevant to your change; for a
cross-cutting change, use the repository's normal Go and Flutter test
commands:

    go test ./...
    go vet ./...
    cd ui/flutter_app
    flutter analyze --no-pub
    flutter test --no-pub

Do not include real provider credentials, refresh tokens, captured private
conversation data, recovery keys, or private certificates in issues, tests,
fixtures, or pull requests. Use synthetic accounts and local test servers.

## Licensing

ViberMate is distributed under the GNU Affero General Public License version
3.0 (AGPLv3). Contributions must be compatible with the repository's
third-party notices and must not silently add a dependency with incompatible
terms.

By submitting code, documentation, tests, or other material, you confirm that
you have the right to submit it and that you accept the Contributor License
Agreement in CLA.md. The CLA does not take your copyright away. It gives the
project maintainer the perpetual, worldwide, non-exclusive, royalty-free
permissions needed to distribute your contribution under the AGPLv3 and,
where separately agreed, under a Commercial License. This lets the project
remain open source while preserving the option to offer commercial licensing
to customers who need rights outside the AGPLv3.

If your employer or another organization owns the work, obtain its approval
before submitting. Identify third-party code or assets clearly and retain
their required notices.

## Pull requests

Explain what changed, how it was tested, and any deployment, security,
retention, or migration impact. Changes to authentication, certificates,
provider credentials, evidence retention, or license terms need explicit
tests and documentation.

For security issues, follow SECURITY.md instead of opening a public issue.
