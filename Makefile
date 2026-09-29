VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
MODULE  := spark-mini-dash
LDFLAGS := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)
TARGETS := linux/arm64 linux/amd64 windows/amd64 darwin/arm64
NATIVE_ARCH := $(shell dpkg --print-architecture 2>/dev/null || uname -m)

.PHONY: fmt vet test build build-all deb deb-all dist run-agent run-dash run-local clean

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/native/spark-agent ./cmd/spark-agent
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/native/spark-dash ./cmd/spark-dash

# Cross-compile for every supported platform. CGO_ENABLED=0 keeps the
# binaries fully static; the web UI is embedded.
build-all:
	@mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; \
		if [ $$os = windows ]; then ext=.exe; fi; \
		echo "== $$os/$$arch =="; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$$os-$$arch/spark-agent$$ext ./cmd/spark-agent; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$$os-$$arch/spark-dash$$ext ./cmd/spark-dash; \
	done

dist: build-all
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		cp README.md deploy/config.example.json dist/$$os-$$arch/; \
		cp deploy/*.service dist/$$os-$$arch/; \
		(cd dist && tar czf $(MODULE)-$$os-$$arch.tar.gz $$os-$$arch); \
	done
	@ls -lh dist/*.tar.gz

# Self-contained Ubuntu .deb installers (native arch). Installing one drops
# the binary, registers the systemd unit, and starts the service.
deb:
	bash scripts/build-deb.sh $(NATIVE_ARCH) spark-agent
	bash scripts/build-deb.sh $(NATIVE_ARCH) spark-dash

deb-all:
	bash scripts/build-deb.sh arm64 spark-agent
	bash scripts/build-deb.sh arm64 spark-dash
	bash scripts/build-deb.sh amd64 spark-agent
	bash scripts/build-deb.sh amd64 spark-dash
	@ls -lh dist/deb/*.deb

run-agent:
	go run ./cmd/spark-agent

run-dash:
	go run ./cmd/spark-dash

# Real-hardware demo on a DGX Spark: agent (live collectors) + dashboard.
run-local:
	./scripts/run-local.sh

clean:
	rm -rf dist
