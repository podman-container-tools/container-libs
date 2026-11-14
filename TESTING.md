# Testing changes against Podman and Buildah

This monorepo containing `common`, `storage`, and `image` is a core dependency of both
[Podman](https://github.com/podman-container-tools/podman) and [Buildah](https://github.com/podman-container-tools/buildah). Before opening a PR, you should verify that your change
doesn't break either downstream project. Two `make` targets are provided to automate this:

Target Steps - `make test-dependencies-podman` and `make test-dependencies-buildah`:
1.  Create `/tmp/test-dependencies` directory if it doesn't exist. Then clone respective
    repository into `/tmp/test-dependencies`
2.  Point that repository's `go.mod` at your local checkout of `common`, `storage`, and
    `image` via `go mod edit -replace`
3.  Vendor the replaced modules and build the project
4.  Run the project's integration and unit tests

## Requirements
These targets run directly on your host and require the following tools to be installed:
    -   `crun`
    -   `conmon`
    -   `netavark`
    -   `aardvark-dns`
    -   `catatonit`
    -   `fuse-overlayfs`
    -   `slirp4netns`
    -   `bats`
    -   `go`
    -   `git`
    -   `podman`
    -   `iptables`

Use `make test-dependencies-check` to make sure they're installed.

## Clearing
To remove the cloned repositories run `make test-dependencies-clean`.
