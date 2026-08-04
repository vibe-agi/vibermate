.PHONY: check check-format check-generated check-dependencies check-structural check-workflows check-release-build check-desktop check-desktop-ui check-desktop-rust test test-race vet vuln vuln-go vuln-rust vuln-ui

check: check-format check-generated check-dependencies check-structural check-workflows check-release-build check-desktop

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

check-workflows:
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
	node --test .github/check-action-pins.test.mjs
	node .github/check-action-pins.mjs

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
	pnpm --dir ui/desktop exec playwright install chromium
	pnpm --dir ui/desktop check:browser

check-desktop-rust:
	pnpm --dir ui/desktop prepare:desktop
	cargo fmt --manifest-path ui/desktop/src-tauri/Cargo.toml --check
	cargo clippy --locked --all-targets --manifest-path ui/desktop/src-tauri/Cargo.toml -- -D warnings
	cargo test --locked --manifest-path ui/desktop/src-tauri/Cargo.toml

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

vuln: vuln-go vuln-rust vuln-ui

vuln-go:
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

vuln-rust:
	@for target in aarch64-apple-darwin x86_64-apple-darwin; do \
		if ! target_glib="$$(cargo tree --locked --manifest-path ui/desktop/src-tauri/Cargo.toml --target "$$target" -i glib)"; then \
			echo "could not inspect glib reachability in $$target"; \
			exit 1; \
		fi; \
		if [ -n "$$target_glib" ]; then \
			echo "RUSTSEC-2024-0429 is reachable in $$target"; \
			exit 1; \
		fi; \
	done
	cargo audit --file ui/desktop/src-tauri/Cargo.lock --target-arch aarch64 --target-os macos --deny unsound --ignore RUSTSEC-2024-0429
	cargo audit --file ui/desktop/src-tauri/Cargo.lock --target-arch x86_64 --target-os macos --deny unsound --ignore RUSTSEC-2024-0429

vuln-ui:
	pnpm --dir ui/desktop audit --prod --audit-level high
