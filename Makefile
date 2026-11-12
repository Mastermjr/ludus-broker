BINARY := viberange-broker
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)

.PHONY: all test build release-linux-amd64 ludus-plugin lint clean

all: test build

test:
	CGO_ENABLED=0 go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/viberange-broker

release-linux-amd64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/$(BINARY)-linux-amd64 ./cmd/viberange-broker

ludus-plugin:
	@test -n "$(LUDUS_API_SOURCE)" || (echo "LUDUS_API_SOURCE is required" >&2; exit 2)
	mkdir -p dist
	ops/ludus-plugin/build-wireguard-plugin.sh "$(LUDUS_API_SOURCE)" dist/viberange-wireguard.so

lint:
	CGO_ENABLED=0 go vet ./...

clean:
	rm -rf $(BINARY) dist coverage.out
