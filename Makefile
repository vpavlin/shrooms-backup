GO ?= go

## Build the daemon for this machine.
build:
	CGO_ENABLED=0 $(GO) build -trimpath -o shrooms-backup ./cmd/shrooms-backup

## The tests; the end-to-end ones need restic (RESTIC=path, or restic on PATH).
test:
	gofmt -l . | grep . && { echo "gofmt: the files above"; exit 1; } || true
	$(GO) vet ./...
	$(GO) test -race ./...

## shrooms-backup for amd64 and arm64, for the image (Dockerfile).
image-ctx:
	@rm -rf build && mkdir -p build
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -o build/shrooms-backup-$$arch ./cmd/shrooms-backup || exit 1; \
	done

.PHONY: build test image-ctx
