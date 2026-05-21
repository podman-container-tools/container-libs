module go.podman.io/image/v5

go 1.25.6

// Warning: Ensure the "go" and "toolchain" versions match exactly to prevent unwanted auto-updates.
// That generally means there should be no toolchain directive present.

require (
	dario.cat/mergo v1.0.2
	github.com/BurntSushi/toml v1.6.0
	github.com/containers/libtrust v0.0.0-20230121012942-c1716e8a8d01
	github.com/containers/ocicrypt v1.3.0
	github.com/cyberphone/json-canonicalization v0.0.0-20241213102144-19d51d7fe467
	github.com/distribution/reference v0.6.0
	github.com/docker/cli v29.5.1+incompatible
	github.com/docker/distribution v2.8.3+incompatible
	github.com/docker/docker-credential-helpers v0.9.7
	github.com/docker/go-connections v0.7.0
	github.com/hashicorp/go-cleanhttp v0.5.2
	github.com/hashicorp/go-retryablehttp v0.7.8
	github.com/klauspost/compress v1.18.6
	github.com/klauspost/pgzip v1.2.6
	github.com/manifoldco/promptui v0.9.0
	github.com/mattn/go-sqlite3 v1.14.44
	github.com/moby/moby/api v1.54.2
	github.com/moby/moby/client v0.4.1
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	github.com/proglottis/gpgme v0.1.6
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/secure-systems-lab/go-securesystemslib v0.11.0
	github.com/sigstore/fulcio v1.8.5
	github.com/sigstore/sigstore v1.10.6
	github.com/sirupsen/logrus v1.9.4
	github.com/stretchr/testify v1.11.1
	github.com/sylabs/sif/v2 v2.24.0
	github.com/ulikunitz/xz v0.5.15
	github.com/vbauerster/mpb/v8 v8.12.0
	go.etcd.io/bbolt v1.4.3
	go.podman.io/storage v1.63.0
	golang.org/x/crypto v0.51.0
	golang.org/x/oauth2 v0.36.0
	golang.org/x/sync v0.20.0
	golang.org/x/term v0.43.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	cyphar.com/go-pathrs v0.2.4 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/VividCortex/ewma v1.2.0 // indirect
	github.com/acarl005/stripansi v0.0.0-20180116102854-5a71ef0e047d // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/chzyer/readline v1.5.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/containerd/stargz-snapshotter/estargz v0.18.2 // indirect
	github.com/coreos/go-oidc/v3 v3.18.0 // indirect
	github.com/cyphar/filepath-securejoin v0.6.1 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/go-containerregistry v0.21.1 // indirect
	github.com/google/go-intervals v0.0.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/miekg/pkcs11 v1.1.1 // indirect
	github.com/mistifyio/go-zfs/v4 v4.0.0 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/sys/capability v0.4.0 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	github.com/moby/sys/user v0.4.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/opencontainers/runtime-spec v1.3.0 // indirect
	github.com/opencontainers/selinux v1.14.1 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/sigstore/protobuf-specs v0.5.0 // indirect
	github.com/smallstep/pkcs7 v0.1.1 // indirect
	github.com/stefanberger/go-pkcs11uri v0.0.0-20230803200340-78284954bff6 // indirect
	github.com/tchap/go-patricia/v2 v2.3.3 // indirect
	github.com/vbatts/tar-split v0.12.3 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.68.0 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/grpc v1.80.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
