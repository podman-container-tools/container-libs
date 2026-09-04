go 1.26.0

// Warning: Ensure the "go" and "toolchain" versions match exactly to prevent unwanted auto-updates.
// That generally means there should be no toolchain directive present.
module go.podman.io/storage

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/containerd/stargz-snapshotter/estargz v0.18.2
	github.com/cyphar/filepath-securejoin v0.7.0
	github.com/docker/go-units v0.5.0
	github.com/google/go-intervals v0.0.2
	github.com/json-iterator/go v1.1.12
	github.com/klauspost/compress v1.20.0
	github.com/klauspost/pgzip v1.2.6
	github.com/mattn/go-shellwords v1.0.14
	github.com/mistifyio/go-zfs/v4 v4.0.0
	github.com/moby/sys/capability v0.4.0
	github.com/moby/sys/mountinfo v0.7.2
	github.com/moby/sys/user v0.4.1
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	github.com/opencontainers/runtime-spec v1.3.0
	github.com/opencontainers/selinux v1.15.1
	github.com/sirupsen/logrus v1.10.2
	github.com/stretchr/testify v1.12.1
	github.com/tchap/go-patricia/v2 v2.3.3
	github.com/ulikunitz/xz v0.5.16
	github.com/varlink/go v0.4.1-0.20260709160413-86facf17ea15
	github.com/vbatts/tar-split v0.12.3
	golang.org/x/sync v0.22.0
	golang.org/x/sys v0.47.0
)

require (
	cyphar.com/go-pathrs v0.2.5 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)
