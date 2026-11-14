GO ?= go
NPM ?= npm

.PHONY: fmt fmt-check vet test race integration ui-test ui-build browser release check
fmt:
	gofmt -w cmd internal tests

fmt-check:
	@test -z "$$(gofmt -l cmd internal tests)"

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

integration:
	$(GO) test -tags=integration ./tests/integration/...

ui-test:
	$(NPM) --prefix ui run typecheck
	$(NPM) --prefix ui test

ui-build:
	$(NPM) --prefix ui run build

browser:
	$(NPM) --prefix ui run test:e2e

# Only run on a controlled Mac with dedicated disposable UTM fixtures.
release:
	@test "$(SHIFTBOX_UTM_RELEASE)" = "1" || (echo 'Set SHIFTBOX_UTM_RELEASE=1 on the controlled release host'; exit 1)
	$(GO) test -tags=utm_release ./tests/release/...

check: fmt-check vet test race ui-test ui-build
