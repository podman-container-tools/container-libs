# This must be sourced from other scripts to work.

OS_RELEASE_VER="$(source /etc/os-release; echo $VERSION_ID | tr -d '.')"
OS_RELEASE_ID="$(source /etc/os-release; echo $ID)"
OS_REL_VER="$OS_RELEASE_ID-$OS_RELEASE_VER"

function die() {
    echo "$1" >&2
    exit 1
}

function parse_args() {
    # module name: storage, image, image-skopeo, common
    MODULE=${1:?must give module as first argument}
    # distro: fedora-current, debian-current
    DISTRO_NAME=${2:?must give distro as second argument}
    # variant: driver for storage, buildtag for image, unused for common
    VARIANT=${3:-}

    validate_module "$MODULE"
    validate_distro "$DISTRO_NAME"
    validate_variant "$MODULE" "$VARIANT"
}

function validate_module() {
    case "$1" in
        "storage"|"image"|"image-skopeo"|"common")
            ;;
        *)
            die "Unknown MODULE '$1', expected: storage, image, image-skopeo, common"
            ;;
    esac
}

function validate_distro() {
    case "$1" in
        "fedora-current"|"debian-current")
            ;;
        *)
            die "Unknown DISTRO_NAME '$1', expected: fedora-current, debian-current"
            ;;
    esac
}

function validate_variant() {
    local module="$1"
    local variant="$2"
    case "$module" in
        storage)
            case "$variant" in
                "vfs"|"overlay"|"overlay-transient"|"fuse-overlay"|"fuse-overlay-whiteout"|"btrfs")
                    ;;
                *)
                    die "Unknown storage variant '$variant', expected: vfs, overlay, overlay-transient, fuse-overlay, fuse-overlay-whiteout, btrfs"
                    ;;
            esac
            ;;
        image|image-skopeo)
            case "$variant" in
                ""|"default"|"openpgp"|"sequoia")
                    ;;
                *)
                    die "Unknown image variant '$variant', expected: default, openpgp, sequoia"
                    ;;
            esac
            ;;
        common)
            ;;
    esac
}
