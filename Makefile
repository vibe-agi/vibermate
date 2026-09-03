.PHONY: check check-format check-generated check-dependencies check-structural check-workflows check-release-tooling check-release-build check-desktop check-flutter check-flutter-macos build-flutter-app test test-race vet vuln vuln-go

check: check-format check-generated check-dependencies check-structural check-workflows check-release-tooling check-release-build check-desktop

check-format:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "$$unformatted"; \
		exit 1; \
	fi

check-generated:
	go generate ./...
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

check-release-tooling:
	node --test ui/flutter_app/tool/desktop_build_manifest.test.mjs tool/macos-release/*.test.mjs

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

check-desktop: check-flutter

check-flutter:
	ui/flutter_app/tool/verify_flutter_sdk.sh
	cd ui/flutter_app && flutter pub get
	cd ui/flutter_app && flutter analyze
	cd ui/flutter_app && flutter test

check-flutter-macos: check-flutter
	cd ui/flutter_app && flutter build macos --debug --config-only
	cd ui/flutter_app && xcodebuild test -quiet -workspace macos/Runner.xcworkspace -scheme Runner -configuration Debug -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO
	ui/flutter_app/tool/build_macos_app.sh live
	cd ui/flutter_app && VIBERMATE_LIVE_TEST_DAEMON="$(CURDIR)/dist/ViberMate.app/Contents/MacOS/vibermated" VIBERMATE_LIVE_TEST_COMMAND="$(CURDIR)/dist/ViberMate.app/Contents/MacOS/vibermate" flutter test test/live_runtime_test.dart
	VIBERMATE_LIVE_TEST_APP="$(CURDIR)/dist/ViberMate.app" go test -count=1 -run '^TestPackagedFlutterDesktopShellLive$$' ./cmd/vibermate-acceptance

build-flutter-app:
	ui/flutter_app/tool/build_macos_app.sh live

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

vuln: vuln-go

vuln-go:
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
