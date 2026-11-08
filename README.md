# container/container-libs

This repository is a **monorepo** combining several core Go libraries and utilities from the [containers](https://github.com/containers) project.  

It brings together:  

- **[`common`](./common/)** → Shared Go code and configuration used across multiple containers projects.  
- **[`storage`](./storage/)** → A Go library for managing container images, layers, and containers.  
- **[`image`](./image/)** → A Go library for interacting with container images and registries.  

These components are used by major container tools such as [Podman](https://github.com/containers/podman), [Buildah](https://github.com/containers/buildah), [CRI-O](https://github.com/cri-o/cri-o), and [Skopeo](https://github.com/containers/skopeo).


---

## Building

Each subproject has its own README.md file with more instructions.

## Contributing

We welcome contributions!

See the **[`CONTRIBUTING.md`](CONTRIBUTING.md)** and **[`CONTRIBUTING_GO.md`](https://github.com/podman-container-tools/community/blob/main/CONTRIBUTING_GO.md)** files.

## Code of Conduct

See the **[`CODE-OF-CONDUCT.md`](CODE-OF-CONDUCT.md)** file.

## Security and Disclosure Information Policy

See the **[`SECURITY.md`](SECURITY.md)** file.


## License

- Apache License 2.0
- SPDX-License-Identifier: Apache-2.0
