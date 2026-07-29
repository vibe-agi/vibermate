.PHONY: check check-format check-generated check-dependencies check-structural test test-race vet vuln

check: check-format check-generated check-dependencies check-structural

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

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
