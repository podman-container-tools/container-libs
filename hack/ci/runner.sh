#!/usr/bin/env bash

SKOPEO_CI_BRANCH=cherry-pick-e894dac8-release-1.22

# This script is only intended to be run inside the Lima VM to configure it and start the tests.
# Do not run locally.

set -eo pipefail

export PATH="/usr/sbin:/usr/local/sbin:$PATH"

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" && pwd )

source "$SCRIPT_DIR/lib.sh"

MODULE=${1:?must give module as first argument}

parse_args "$@"

###############################################################################
# Environment preparation
###############################################################################

prepare_storage_env() {
    for i in $(seq 0 1023); do
        [ -e /dev/loop$i ] || sudo mknod /dev/loop$i b 7 $i 2>/dev/null || true
    done
}

###############################################################################
# Test runners
###############################################################################

run_storage() {
    cd storage
    make local-binary

    SUDO="sudo -E env PATH=$PATH GOPATH=$(go env GOPATH) HOME=$HOME"

    case "$VARIANT" in
        overlay)
            $SUDO make STORAGE_DRIVER=overlay local-test-integration local-test-unit
            ;;
        overlay-transient)
            $SUDO make STORAGE_DRIVER=overlay STORAGE_TRANSIENT=1 local-test-integration local-test-unit
            ;;
        fuse-overlay)
            $SUDO make STORAGE_DRIVER=overlay STORAGE_OPTION=overlay.mount_program=/usr/bin/fuse-overlayfs local-test-integration local-test-unit
            ;;
        fuse-overlay-whiteout)
            $SUDO FUSE_OVERLAYFS_DISABLE_OVL_WHITEOUT=1 make STORAGE_DRIVER=overlay STORAGE_OPTION=overlay.mount_program=/usr/bin/fuse-overlayfs local-test-integration local-test-unit
            ;;
        vfs)
            $SUDO make STORAGE_DRIVER=vfs local-test-integration local-test-unit
            ;;
        btrfs)
            if [[ "$(./hack/btrfs_tag.sh)" =~ exclude_graphdriver_btrfs ]]; then
                echo "Built without btrfs, so we can't test it"
                exit 1
            fi
            if ! grep -q "	btrfs$" /proc/filesystems; then
                sudo modprobe btrfs || true
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
            trap "sudo umount -l $tmpdir; rm -f btrfs.img" EXIT
            truncate -s 0 btrfs.img
            fallocate -l 1G btrfs.img
            sudo mkfs.btrfs btrfs.img
            sudo mount -o loop btrfs.img $tmpdir
            $SUDO TMPDIR="$tmpdir" make STORAGE_DRIVER=btrfs local-test-integration local-test-unit
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

    git config --global --add safe.directory "$GOSRC"

    # Run root tests for storage-dependent tests

    # Hacky solution to find test that must be run as root.
    # This looks for the ensureTestCanCreateImages() test function call and gets the
    # function name where it is called via git grep,
    # then trims the line to only show the actual function name and add "^$" around it
    # since go test commands only accepts a single regex.
    # Then join all names with "|" with paste to again build up a single regex string
    # that matches all these names.
    #
    # test_filter must have the $ duplicated because make expands the value
    # (and there seems to be no trivial way to avoid that while defining the variable
    # as an argument?!)
    test_filter=$(git grep -h --show-function ensureTestCanCreateImages ./storage |
        sed -n 's/func \(Test[[:alnum:]]*\)(.*/^\1$$/p' |
        paste -sd "|" -)
    if [ -n "$test_filter" ]; then
        sudo -E env "PATH=$PATH" "GOPATH=$GOPATH_DIR" \
            make test "BUILDTAGS=$BUILDTAGS" "TESTFLAGS=-v -run '$test_filter'" TEST_PACKAGES=./storage
    fi

    # Restore permissions
    sudo chown -R $(id -u):$(id -g) "$GOPATH_DIR"

    # Run rootless tests
    cleanup() {
        $GOSRC/image/signature/sigstore/rekor/testdata/start-rekor.sh ci remove || true
    }
    trap cleanup EXIT

    # start custom rekor which is needed by the tests
    $GOSRC/image/signature/sigstore/rekor/testdata/start-rekor.sh ci
    make test BUILDTAGS='$BUILDTAGS' TESTFLAGS=-v REKOR_SERVER_URL='http://127.0.0.1:3000'
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
    AUTOMATION_RELEASE="${AUTOMATION_RELEASE:-$(get_automation_release)}"
    SKOPEO_CIDEV_CONTAINER_FQIN="ghcr.io/podman-container-tools/skopeo_cidev:$AUTOMATION_RELEASE"

    sudo podman pull --quiet "$SKOPEO_CIDEV_CONTAINER_FQIN"
    ctr_id=$(sudo podman create "$SKOPEO_CIDEV_CONTAINER_FQIN")
    mnt=$(sudo podman mount "$ctr_id")
    sudo cp -a "$mnt/usr/local/bin/." /usr/local/bin/
    sudo mkdir -p /registry
    sudo cp -a "$mnt/atomic-registry-config.yml" /
    sudo podman umount --latest
    sudo podman rm --latest

    git clone -b "$SKOPEO_CI_BRANCH" https://github.com/QiWang19/skopeo.git "$SKOPEO_PATH"
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
    NETAVARK_BINARY=/usr/libexec/podman/netavark
    export NETAVARK_BINARY

    make build
    make build-cross

    sudo -E env "PATH=$PATH" "GOPATH=$(go env GOPATH)" "HOME=$HOME" \
        make test
}

###############################################################################
# Main dispatch
###############################################################################


# Normalize module name for function dispatch (image-skopeo -> image_skopeo)
MODULE_FUNC="${MODULE//-/_}"

if type -t prepare_${MODULE_FUNC}_env &>/dev/null; then
    echo "::group::Preparing environment for $MODULE"
    prepare_${MODULE_FUNC}_env
    echo "::endgroup::"
fi

echo "::group::Logging system info"
"$SCRIPT_DIR/logcollector.sh" packages
"$SCRIPT_DIR/logcollector.sh" ip
echo "::endgroup::"

echo "Starting tests: $MODULE $VARIANT"
run_${MODULE_FUNC}
