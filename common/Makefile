export GOPROXY=https://proxy.golang.org

GO ?= go
GO_BUILD=$(GO) build
BUILDTAGS := containers_image_openpgp,systemd
DESTDIR ?=
PREFIX := /usr/local
CONFIGDIR := ${PREFIX}/share/containers

define go-build
	CGO_ENABLED=0 \
	GOOS=$(1) GOARCH=$(2) $(GO) build -tags "$(3)" ./...
endef

ifeq ($(shell uname -s),Linux)
define go-build-c
	CGO_ENABLED=1 \
	GOOS=$(1) GOARCH=$(2) $(GO) build -tags "$(3)" ./...
endef
else
define go-build-c
endef
endif

.PHONY:
build-cross:
	$(call go-build-c,linux) # attempt to build without tags
	$(call go-build-c,linux,,${BUILDTAGS})
	$(call go-build,linux,386,${BUILDTAGS})
	$(call go-build-c,linux) # attempt to build without tags
	$(call go-build,linux,386,${BUILDTAGS})
	$(call go-build,linux,arm,${BUILDTAGS})
	$(call go-build,linux,arm64,${BUILDTAGS})
	$(call go-build,linux,ppc64le,${BUILDTAGS})
	$(call go-build,linux,s390x,${BUILDTAGS})
	$(call go-build,darwin,amd64,${BUILDTAGS})
	$(call go-build,windows,amd64,${BUILDTAGS})
	$(call go-build,windows,386,${BUILDTAGS})
	$(call go-build,freebsd,amd64,${BUILDTAGS})
	$(call go-build,freebsd,386,${BUILDTAGS})

.PHONY: all
all: build-amd64 build-386

.PHONY: build
build: build-amd64 build-386

.PHONY: build-amd64
build-amd64:
	GOARCH=amd64 $(GO_BUILD) -tags $(BUILDTAGS) ./...

.PHONY: build-386
build-386:
ifneq ($(shell uname -s), Darwin)
	GOARCH=386 $(GO_BUILD) -tags $(BUILDTAGS) ./...
endif

.PHONY: bin/netavark-testplugin
bin/netavark-testplugin:
	$(GO_BUILD) -o $@ ./libnetwork/netavark/testplugin/

.PHONY: netavark-testplugin
netavark-testplugin: bin/netavark-testplugin

.PHONY: docs
docs:
	$(MAKE) -C docs

# Match the historical common lint tag set (kept out of the shared config so
# image/storage can pass their own tags via --build-tags).
LINT_BUILD_TAGS ?= apparmor,seccomp,selinux,systemd,exclude_graphdriver_btrfs,containers_image_openpgp

.PHONY: lint
lint:
	golangci-lint run --config ../.golangci.yml --build-tags "$(LINT_BUILD_TAGS)"

.PHONY: fmt
fmt:
	golangci-lint fmt --config ../.golangci.yml

.PHONY: validate
validate: lint
	./tools/validate_seccomp.sh ./pkg/seccomp

.PHONY: tidy-in-container
tidy-in-container:
	podman run --privileged --rm --env HOME=/root -v `pwd`:/src -w /src golang make tidy

.PHONY: tidy
tidy:
	$(GO) mod tidy
	$(GO) mod verify
	$(GO) mod edit -toolchain none

.PHONY: install
install:
	install -d ${DESTDIR}/${CONFIGDIR}
	install -m 0644 pkg/config/containers.conf ${DESTDIR}/${CONFIGDIR}/containers.conf

	$(MAKE) -C docs install

.PHONY: test
test: test-unit

.PHONY: test-unit
test-unit: netavark-testplugin
	go test --tags seccomp,$(BUILDTAGS) -v ./...
	go test --tags remote,$(BUILDTAGS) -v ./pkg/config

.PHONY: bin/json-proxy-test-server
bin/json-proxy-test-server:
	$(GO_BUILD) -o $@ ./cmd/json-proxy-test-server/

.PHONY: test-integration
test-integration: bin/json-proxy-test-server
	JSON_PROXY_TEST_BINARY=$(CURDIR)/bin/json-proxy-test-server go test --tags $(BUILDTAGS) -v ./pkg/json-proxy/

clean: ## Clean artifacts
	$(MAKE) -C docs clean
	find . -name \*~ -delete
	find . -name \#\* -delete

.PHONY: seccomp.json
seccomp.json: $(sources)
	$(GO) run ./cmd/seccomp/generate.go
