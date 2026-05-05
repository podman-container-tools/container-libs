#!/usr/bin/env bash

# This script is only intended to be run inside the Lima VM to configure it and start the tests.
# Do not run locally.

set -eo pipefail

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" && pwd )

source "$SCRIPT_DIR/lib.sh"

MODULE=${1:?must give module as first argument}

parse_args "$@"

###############################################################################
# Dependency installation
###############################################################################

install_deps_storage() {
    case $OS_RELEASE_ID in
        fedora)
            dnf install -y \
                git golang bats \
                btrfs-progs btrfs-progs-devel \
                gpgme-devel libassuan-devel device-mapper-devel \
                fuse-overlayfs fuse3 \
                gcc make kmod util-linux e2fsprogs
            dnf remove -y gcc-go || true
            ;;
        debian)
            apt-get update
            apt-get install -y \
                git golang bats \
                btrfs-progs libbtrfs-dev \
                libgpgme-dev libassuan-dev libdevmapper-dev \
                fuse-overlayfs fuse3 \
                gcc make pkg-config kmod e2fsprogs util-linux bzip2
            ;;
        *) die "Unsupported OS for storage: $OS_RELEASE_ID" ;;
    esac
}

install_deps_image() {
    case $OS_RELEASE_ID in
        fedora)
            dnf install -y \
                git golang \
                gpgme-devel libassuan-devel btrfs-progs-devel device-mapper-devel \
                subid-devel \
                podman openssh-server \
                gcc make
            dnf remove -y gcc-go || true
            ;;
        debian)
            apt-get update
            apt-get install -y \
                git golang \
                libgpgme-dev libassuan-dev libbtrfs-dev libdevmapper-dev \
                libsubid-dev \
                podman openssh-server \
                gcc make pkg-config
            ;;
        *) die "Unsupported OS for image: $OS_RELEASE_ID" ;;
    esac
    printf 'unqualified-search-registries = ["docker.io"]\n' | sudo tee /etc/containers/registries.conf

    if [[ "$VARIANT" == "sequoia" ]]; then
        case $OS_RELEASE_ID in
            fedora) dnf install -y openssl-devel capnproto ;;
            debian) apt-get install -y libssl-dev capnproto ;;
        esac
        curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain stable
        source "$HOME/.cargo/env"
        git clone --depth=1 https://github.com/ueno/podman-sequoia.git /tmp/podman-sequoia
        make -C /tmp/podman-sequoia
        case $OS_RELEASE_ID in
            fedora) sudo cp /tmp/podman-sequoia/target/release/libpodman_sequoia.so /usr/lib64/ ;;
            debian) sudo cp /tmp/podman-sequoia/target/release/libpodman_sequoia.so /usr/lib/x86_64-linux-gnu/ ;;
        esac
        sudo ldconfig
    fi
}

install_deps_image_skopeo() {
    install_deps_image
    case $OS_RELEASE_ID in
        fedora)
            dnf install -y bats go-md2man docker-distribution httpd-tools
            ;;
        debian)
            apt-get install -y bats go-md2man docker-registry apache2-utils
            ;;
    esac
    echo "root:100000:65536" | sudo tee -a /etc/subuid
    echo "root:100000:65536" | sudo tee -a /etc/subgid
    sudo ln -sf /usr/bin/docker-registry /usr/local/bin/registry 2>/dev/null || true
}

install_deps_common() {
    case $OS_RELEASE_ID in
        fedora)
            dnf install -y \
                git golang \
                libseccomp-devel gpgme-devel btrfs-progs-devel device-mapper-devel \
                subid-devel \
                podman protobuf-compiler protobuf-devel \
                gcc make
            dnf remove -y gcc-go || true
            ;;
        debian)
            apt-get update
            apt-get install -y \
                git golang \
                libseccomp-dev libgpgme-dev libbtrfs-dev libdevmapper-dev \
                libsubid-dev \
                podman protobuf-compiler libprotobuf-dev \
                gcc make pkg-config
            ;;
        *) die "Unsupported OS for common: $OS_RELEASE_ID" ;;
    esac
    printf 'unqualified-search-registries = ["docker.io"]\n' | sudo tee /etc/containers/registries.conf

    curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain stable
    source "$HOME/.cargo/env"
    git clone --depth=1 https://github.com/containers/netavark.git /tmp/netavark-src
    make -C /tmp/netavark-src build
    sudo mkdir -p /usr/local/libexec/podman
    sudo install -m 755 /tmp/netavark-src/bin/netavark /usr/local/libexec/podman/netavark
}

###############################################################################
# Environment preparation
###############################################################################

prepare_storage_env() {
    truncate -s 10G /var/tmp/test-fs.img
    mkfs.ext4 -q /var/tmp/test-fs.img
    mount -o loop /var/tmp/test-fs.img /tmp

    for i in $(seq 0 1023); do
        [ -e /dev/loop$i ] || mknod /dev/loop$i b 7 $i 2>/dev/null || true
    done
}

prepare_image_env() {
    ROOTLESS_USER="testuser$$"
    rootless_uid=$((RANDOM+1000))
    rootless_gid=$((RANDOM+1000))
    groupadd -g $rootless_gid $ROOTLESS_USER
    useradd -g $rootless_gid -u $rootless_uid --no-user-group --create-home $ROOTLESS_USER

    chown -R $ROOTLESS_USER:$ROOTLESS_USER "$(go env GOPATH)"
    chown -R $ROOTLESS_USER:$ROOTLESS_USER "$(pwd)"

    mkdir -p "/run/user/$rootless_uid"
    chown $ROOTLESS_USER:$ROOTLESS_USER "/run/user/$rootless_uid"

    mkdir -p /root/.ssh "/home/$ROOTLESS_USER/.ssh"
    ssh-keygen -t ed25519 -P "" -f /root/.ssh/id_ed25519
    cat /root/.ssh/*.pub >> "/home/$ROOTLESS_USER/.ssh/authorized_keys"
    chmod -R 700 /root/.ssh "/home/$ROOTLESS_USER/.ssh"
    chown -R $ROOTLESS_USER:$ROOTLESS_USER "/home/$ROOTLESS_USER/.ssh"

    systemctl start sshd || systemctl start ssh
    ssh-keyscan localhost > /root/.ssh/known_hosts

    export ROOTLESS_USER rootless_uid
}

###############################################################################
# Test runners
###############################################################################

run_storage() {
    cd storage
    make local-binary

    case "$VARIANT" in
        overlay)
            make STORAGE_DRIVER=overlay local-test-integration local-test-unit
            ;;
        overlay-transient)
            make STORAGE_DRIVER=overlay STORAGE_TRANSIENT=1 local-test-integration local-test-unit
            ;;
        fuse-overlay)
            make STORAGE_DRIVER=overlay STORAGE_OPTION=overlay.mount_program=/usr/bin/fuse-overlayfs local-test-integration local-test-unit
            ;;
        fuse-overlay-whiteout)
            FUSE_OVERLAYFS_DISABLE_OVL_WHITEOUT=1 make STORAGE_DRIVER=overlay STORAGE_OPTION=overlay.mount_program=/usr/bin/fuse-overlayfs local-test-integration local-test-unit
            ;;
        vfs)
            make STORAGE_DRIVER=vfs local-test-integration local-test-unit
            ;;
        btrfs)
            if [[ "$(./hack/btrfs_tag.sh)" =~ exclude_graphdriver_btrfs ]]; then
                echo "Built without btrfs, so we can't test it"
                exit 1
            fi
            if ! grep -q "	btrfs$" /proc/filesystems; then
                modprobe btrfs || true
                if ! grep -q "	btrfs$" /proc/filesystems; then
                    echo "Kernel does not support btrfs"
                    exit 1
                fi
            fi
            if ! command -v mkfs.btrfs &> /dev/null; then
                echo "mkfs.btrfs not installed"
                exit 1
            fi
            tmpdir=$(mktemp -d)
            trap "umount -l $tmpdir; rm -f btrfs.img" EXIT
            truncate -s 0 btrfs.img
            fallocate -l 1G btrfs.img
            mkfs.btrfs btrfs.img
            mount -o loop btrfs.img $tmpdir
            TMPDIR="$tmpdir" make STORAGE_DRIVER=btrfs local-test-integration local-test-unit
            ;;
        *)
            die "Unknown storage variant: $VARIANT"
            ;;
    esac
}

run_image() {
    cd image

    local BUILDTAGS=""
    case "$VARIANT" in
        default|"") BUILDTAGS="" ;;
        openpgp) BUILDTAGS="containers_image_openpgp" ;;
        sequoia) BUILDTAGS="containers_image_sequoia" ;;
    esac

    GOPATH_DIR="$(go env GOPATH)"
    GOROOT_DIR="$(go env GOROOT)"
    GOSRC="$(cd .. && pwd)"

    # Run root tests for storage-dependent tests
    test_filter=$(git grep -h --show-function ensureTestCanCreateImages ../storage |
        sed -n 's/func \(Test[[:alnum:]]*\)(.*/^\1$/p' |
        paste -sd "|" -)
    if [ -n "$test_filter" ]; then
        make test "BUILDTAGS=$BUILDTAGS" "TESTFLAGS=-v -run '$test_filter'" TEST_PACKAGES=./storage
    fi

    # Run rootless tests
    cleanup() {
        ssh -o StrictHostKeyChecking=no -i /root/.ssh/id_ed25519 \
            $ROOTLESS_USER@localhost \
            "export XDG_RUNTIME_DIR=/run/user/$rootless_uid && export PATH=$GOROOT_DIR/bin:\$PATH && bash $GOSRC/image/signature/sigstore/rekor/testdata/start-rekor.sh ci remove" || true
        chown -R $(id -u):$(id -g) "$GOPATH_DIR" "$GOSRC"
    }
    trap cleanup EXIT

    ssh -o StrictHostKeyChecking=no -i /root/.ssh/id_ed25519 \
        $ROOTLESS_USER@localhost \
        "export XDG_RUNTIME_DIR=/run/user/$rootless_uid && export PATH=$GOROOT_DIR/bin:\$PATH && export GOPATH=$GOPATH_DIR && bash $GOSRC/image/signature/sigstore/rekor/testdata/start-rekor.sh ci"

    ssh -o StrictHostKeyChecking=no -i /root/.ssh/id_ed25519 \
        $ROOTLESS_USER@localhost \
        "export XDG_RUNTIME_DIR=/run/user/$rootless_uid && export PATH=$GOROOT_DIR/bin:\$PATH && export GOPATH=$GOPATH_DIR && cd $GOSRC/image && make test BUILDTAGS='$BUILDTAGS' TESTFLAGS=-v REKOR_SERVER_URL='http://127.0.0.1:3000'"
}

run_image_skopeo() {
    local BUILDTAGS=""
    case "$VARIANT" in
        default|"") BUILDTAGS="" ;;
        openpgp) BUILDTAGS="containers_image_openpgp" ;;
        sequoia) BUILDTAGS="containers_image_sequoia" ;;
    esac

    GOSRC="$(pwd)"
    SKOPEO_PATH="/var/tmp/skopeo"
    SKOPEO_CIDEV_CONTAINER_FQIN="quay.io/libpod/skopeo_cidev:latest"

    sudo podman pull --quiet "$SKOPEO_CIDEV_CONTAINER_FQIN"
    mnt=$(sudo podman mount $(sudo podman create "$SKOPEO_CIDEV_CONTAINER_FQIN"))
    sudo cp -a "$mnt/usr/local/bin/." /usr/local/bin/
    sudo mkdir -p /registry
    sudo cp -a "$mnt/atomic-registry-config.yml" /
    sudo podman umount --latest
    sudo podman rm --latest

    git clone -b main https://github.com/containers/skopeo.git "$SKOPEO_PATH"
    cd "$SKOPEO_PATH"
    go mod edit -replace "go.podman.io/storage=$GOSRC/storage"
    go mod edit -replace "go.podman.io/image/v5=$GOSRC/image"
    go mod edit -replace "go.podman.io/common=$GOSRC/common"
    make vendor

    make bin/skopeo "BUILDTAGS=$BUILDTAGS"
    sudo make install PREFIX=/usr/local "BUILDTAGS=$BUILDTAGS"

    make test-unit-local "BUILDTAGS=$BUILDTAGS"

    sudo podman system reset --force
    export SKOPEO_CONTAINER_TESTS=1
    sudo -E env "PATH=/usr/local/bin:$PATH" "GOPATH=$(go env GOPATH)" "SKOPEO_CONTAINER_TESTS=$SKOPEO_CONTAINER_TESTS" \
        make test-integration-local "BUILDTAGS=$BUILDTAGS"

    sudo podman system reset --force
    sudo -E env "PATH=/usr/local/bin:$PATH" "GOPATH=$(go env GOPATH)" "SKOPEO_CONTAINER_TESTS=$SKOPEO_CONTAINER_TESTS" \
        make test-system-local "BUILDTAGS=$BUILDTAGS"
}

run_common() {
    cd common
    NETAVARK_BINARY=/usr/local/libexec/podman/netavark
    export NETAVARK_BINARY

    make build
    make build-cross

    sudo -E env "PATH=$PATH" "GOPATH=$(go env GOPATH)" "HOME=$HOME" \
        make test
    sudo -E env "PATH=$PATH" "GOPATH=$(go env GOPATH)" "HOME=$HOME" \
        make test-integration
}

###############################################################################
# Main dispatch
###############################################################################

echo
echo "#################"
echo "Installing dependencies for $MODULE"
echo "#################"

# Normalize module name for function dispatch (image-skopeo -> image_skopeo)
MODULE_FUNC="${MODULE//-/_}"

install_deps_${MODULE_FUNC}

if type -t prepare_${MODULE_FUNC}_env &>/dev/null; then
    echo
    echo "#################"
    echo "Preparing environment for $MODULE"
    echo "#################"
    prepare_${MODULE_FUNC}_env
fi

echo
echo "#################"
echo "Logging system info"
echo "#################"

"$SCRIPT_DIR/logcollector.sh" packages
"$SCRIPT_DIR/logcollector.sh" ip

echo
echo "#################"
echo "Starting tests: $MODULE $VARIANT"
echo "#################"

run_${MODULE_FUNC}
