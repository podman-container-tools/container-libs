#!/usr/bin/env bash

set -eo pipefail

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" && pwd )

source "$SCRIPT_DIR/lib.sh"

AUTOMATION_RELEASE="${AUTOMATION_RELEASE:-$(get_automation_release)}"
LIMA_VM_NAME=container-libs-ci

MODULE=${1:?must give module as first argument}

REPO_DIR="$SCRIPT_DIR/../.."

parse_args "$@"

IMAGE="$DISTRO_NAME.x86_64.qcow2.zst"

IMAGE_URL="https://objectstorage.us-ashburn-1.oraclecloud.com/n/id0lmbbwgcdv/b/podman-ci-vm-images/o/releases/$AUTOMATION_RELEASE/$IMAGE"

trap 'limactl delete --force $LIMA_VM_NAME' EXIT

echo "::group::Starting VM"
limactl --yes start --plain --name=$LIMA_VM_NAME --cpus $(nproc) --memory 8 --nested-virt \
    --set ".images=[{\"location\":\"$IMAGE_URL\", \"arch\": \"x86_64\"}]" \
    "$SCRIPT_DIR/template.lima.yml"

limactl copy "$REPO_DIR" "$LIMA_VM_NAME:/var/tmp/container-libs"
echo "::endgroup::"

set +e

limactl shell --workdir /var/tmp/container-libs $LIMA_VM_NAME ./hack/ci/runner.sh "${@}"
rc=$?

echo "::group::Collecting logs"
limactl shell --workdir /var/tmp/container-libs $LIMA_VM_NAME sudo hack/ci/logcollector.sh journal &> "$SCRIPT_DIR/journal.log"
limactl shell --workdir /var/tmp/container-libs $LIMA_VM_NAME sudo hack/ci/logcollector.sh audit &> "$SCRIPT_DIR/audit.log"
limactl shell --workdir /var/tmp/container-libs $LIMA_VM_NAME sudo hack/ci/logcollector.sh df &> "$SCRIPT_DIR/df.log"
echo "::endgroup::"

exit $rc
