BINARY  := graf
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/nabooai/grafcli/cmd.Version=$(VERSION)

.PHONY: build install test lint fmt snapshot clean

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) .

install:
	go install -ldflags '$(LDFLAGS)' .

test:
	go test ./...

lint:
	go vet ./...
	@out=$$(gofmt -l . | grep -v '^$$' || true); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

fmt:
	gofmt -w .

snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin dist
