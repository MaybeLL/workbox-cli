.PHONY: build test race release
VERSION ?= 0.2.0

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/workbox ./cmd/workbox

test:
	go test ./...

race:
	go test -race ./...

release:
	go run ./tools/release --version $(VERSION) --output dist
