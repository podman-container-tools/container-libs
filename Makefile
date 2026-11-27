

GO := go
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

export PATH := $(PATH):${GOBIN}

EPOCH_TEST_COMMIT ?= $(shell git merge-base $${DEST_BRANCH:-main} HEAD)


validate: codespell git-validation lint check-ci-yaml

.PHONY: check-ci-yaml
check-ci-yaml:
	hack/ci/ci_yaml_test.py

.PHONY: codespell
codespell:
	codespell --dictionary=-

.PHONY: install.tools
install.tools: .install.gitvalidation .install.golangci-lint .install.md2man

.PHONY: .install.gitvalidation
.install.gitvalidation:
	@if [ ! -x "$(GOBIN)/git-validation" ]; then \
		$(GO) install github.com/vbatts/git-validation@latest; \
	fi

.PHONY: .install.golangci-lint
.install.golangci-lint:
	@if [ ! -x "$(GOBIN)/golangci-lint" ]; then \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b $(GOBIN) \
			$(shell sed -En 's/.*LINT_VERSION:\s(.*)/\1/p' .github/workflows/validate.yml) ; \
	fi

.PHONY: .install.md2man
.install.md2man:
	@if [ ! -x $$(command -v go-md2man)  ] && [ ! -x "$(GOBIN)/go-md2man" ]; then \
		$(GO) install github.com/cpuguy83/go-md2man/v2@latest; \
	fi

.PHONY: git-validation
git-validation: .install.gitvalidation
ifndef EPOCH_TEST_COMMIT
	$(error EPOCH_TEST_COMMIT is empty)
endif
	GIT_CHECK_EXCLUDE="./vendor" git-validation $(if $(CI),,-q) -run DCO,short-subject,dangling-whitespace -range "$(EPOCH_TEST_COMMIT)..HEAD"

.PHONY: lint
lint: .install.golangci-lint
	@$(MAKE) -C common lint
	@$(MAKE) -C image lint
	@$(MAKE) -C storage lint

.PHONY: fmt
fmt: .install.golangci-lint
	@$(MAKE) -C common fmt
	@$(MAKE) -C image fmt
	@$(MAKE) -C storage fmt

.PHONY: vendor-in-container
vendor-in-container:
	podman run --privileged --rm --env HOME=/root -v `pwd`:/src -w /src golang make vendor

.PHONY: vendor
vendor:
	@$(MAKE) -C common tidy
	@$(MAKE) -C image tidy
	@$(MAKE) -C storage tidy
	$(GO) work vendor
	$(GO) work sync

.PHONY: test-dependencies-check
test-dependencies-check:
	@missing=""; \
		for bin in crun conmon go git podman iptables bats fuse-overlayfs slirp4netns; do \
			command -v $$bin >/dev/null 2>&1 || missing="$$missing $$bin"; \
		done; \
		for bin in netavark aardvark-dns catatonit; do \
			test -x /usr/libexec/podman/$$bin || missing="$$missing $$bin"; \
		done; \
		if [ -n "$$missing" ]; then \
			echo "Missing required tools: $$missing"; \
			echo "Install these before running test-dependencies-local"; \
			exit 1; \
		fi
	@echo "All dependencies installed."

.PHONY: test-dependencies-podman
test-dependencies-podman: test-dependencies-check
	if [ ! -d /tmp/test-dependencies/podman ]; then \
		mkdir -p /tmp/test-dependencies; \
		git clone --depth 1 https://github.com/podman-container-tools/podman.git /tmp/test-dependencies/podman; \
	fi
	cd /tmp/test-dependencies/podman && \
		go mod edit -replace go.podman.io/common=$(CURDIR)/common && \
		go mod edit -replace go.podman.io/storage=$(CURDIR)/storage && \
		go mod edit -replace go.podman.io/image/v5=$(CURDIR)/image && \
		go mod tidy && \
		go mod vendor && \
		make podman && \
		make localunit; \
		make localintegration

.PHONY: test-dependencies-buildah
test-dependencies-buildah: test-dependencies-check
	if [ ! -d /tmp/test-dependencies/buildah ]; then \
		mkdir -p /tmp/test-dependencies; \
		git clone --depth 1 https://github.com/podman-container-tools/buildah.git /tmp/test-dependencies/buildah; \
	fi
	cd /tmp/test-dependencies/buildah && \
		go mod edit -replace go.podman.io/common=$(CURDIR)/common && \
		go mod edit -replace go.podman.io/storage=$(CURDIR)/storage && \
		go mod edit -replace go.podman.io/image/v5=$(CURDIR)/image && \
		go mod tidy && \
		go mod vendor && \
		make binaries && \
		make test-unit;	\
		make test-integration

.PHONY: test-dependencies-clean
test-dependencies-clean:
	rm -rf /tmp/test-dependencies
	@echo "Note: this does not prune podman storage/networks created by the test run."
	@echo "Run 'podman system prune --volumes' manually if you want to reclaim that."
