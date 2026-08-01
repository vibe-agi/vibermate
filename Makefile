.PHONY: check check-format check-generated check-dependencies check-structural check-release-build check-desktop check-desktop-ui check-desktop-rust test test-race vet vuln vuln-go vuln-rust

check: check-format check-generated check-dependencies check-structural check-release-build check-desktop

check-format:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "$$unformatted"; \
		exit 1; \
	fi

check-generated:
	go generate ./...
	node ui/desktop/scripts/sync-generated.mjs --check
	git diff --exit-code

check-dependencies:
	go mod tidy -diff

check-structural:
	go run ./cmd/repositorycheck
	go test ./internal/repositorycheck

# The release backend is selected by a build tag, so nothing in an ordinary
# build or test run compiles it. It went missing entirely once; this is what
# would have caught that.
check-release-build:
	go build -tags vibermate_native_secrets ./...
	go vet -tags vibermate_native_secrets ./...
# A platform with no SecretStore refuses at startup rather than degrading, and
# that refusal is a code path like any other: it has to keep compiling.
	GOOS=windows GOARCH=amd64 go build -tags vibermate_native_secrets ./...
	GOOS=linux GOARCH=amd64 go build -tags vibermate_native_secrets ./...

check-desktop: check-desktop-ui check-desktop-rust

check-desktop-ui:
	pnpm --dir ui/desktop install --frozen-lockfile
	pnpm --dir ui/desktop check

check-desktop-rust:
	pnpm --dir ui/desktop prepare:desktop
	cargo fmt --manifest-path ui/desktop/src-tauri/Cargo.toml --check
	cargo test --locked --manifest-path ui/desktop/src-tauri/Cargo.toml

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

vuln: vuln-go vuln-rust

vuln-go:
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

vuln-rust:
	cargo audit --file ui/desktop/src-tauri/Cargo.lock
