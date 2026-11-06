export GOPROXY=https://proxy.golang.org


GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

# when cross compiling _for_ a Darwin or windows host, then we must use openpgp
BUILD_TAGS_WINDOWS_CROSS = containers_image_openpgp
BUILD_TAGS_DARWIN_CROSS = containers_image_openpgp
SEQUOIA_SONAME_DIR =

BUILDTAGS =
BUILDFLAGS := -tags "$(BUILDTAGS)" -ldflags '-X go.podman.io/image/v5/signature/internal/sequoia.sequoiaLibraryDir='"$(SEQUOIA_SONAME_DIR)"

# Extra flags passed to go test
TESTFLAGS :=
TEST_PACKAGES := ./...

PACKAGES := $(shell go list $(BUILDFLAGS) ./...)
SOURCE_DIRS = $(shell echo $(PACKAGES) | awk 'BEGIN{FS="/"; RS=" "}{print $$4}' | uniq)

ifeq ($(shell uname -s),FreeBSD)
CONTAINERSCONFDIR ?= /usr/local/share/containers
else
CONTAINERSCONFDIR ?= /usr/share/containers
endif
REGISTRIESDDIR ?= ${CONTAINERSCONFDIR}/registries.d

export PATH := $(PATH):${GOBIN}

.PHONY: all
all: test lint

.PHONY: build
build:
	go build $(BUILDFLAGS) ./...

.PHONY: docs
docs:
	$(MAKE) -C docs docs

.PHONY: install-docs
install-docs: docs
	$(MAKE) -C docs install

.PHONY: install
install: install-docs
	install -d -m 755 ${DESTDIR}${CONTAINERSCONFDIR}
	install -m 644 default-policy.json ${DESTDIR}${CONTAINERSCONFDIR}/policy.json
	install -d -m 755 ${DESTDIR}${REGISTRIESDDIR}
	install -m 644 default.yaml ${DESTDIR}${REGISTRIESDDIR}/default.yaml

.PHONY: cross
cross:
	GOOS=windows $(MAKE) build BUILDTAGS="$(BUILDTAGS) $(BUILD_TAGS_WINDOWS_CROSS)"
	GOOS=darwin $(MAKE) build BUILDTAGS="$(BUILDTAGS) $(BUILD_TAGS_DARWIN_CROSS)"

.PHONY: clean
clean:
	$(MAKE) -C docs clean

.PHONY: test
test:
	@go test $(BUILDFLAGS) $(TESTFLAGS) -cover $(TEST_PACKAGES)

.PHONY: fmt
fmt:
	golangci-lint fmt

.PHONY: lint
lint:
	golangci-lint run --build-tags "$(BUILDTAGS)"

.PHONY: tidy-in-container
tidy-in-container:
	podman run --privileged --rm --env HOME=/root -v `pwd`:/src -w /src golang make tidy

.PHONY: tidy
tidy:
	go mod tidy
	go mod verify
	go mod edit -toolchain none
