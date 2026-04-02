% containers-certs.d 5 Directory for storing custom container-registry TLS configurations

# NAME
containers-certs.d - Directory for storing custom container-registry TLS configurations

# DESCRIPTION
A custom TLS configuration for a container registry can be configured by creating a directory under `$XDG_CONFIG_HOME/containers/certs.d` (or `$HOME/.config/containers/certs.d` if `XDG_CONFIG_HOME` is unset), `/etc/containers/certs.d`, or `/usr/share/containers/certs.d`.
The name of the directory must correspond to the `host`[`:port`] of the registry (e.g., `my-registry.com:5000`).

Depending on whether the process is running as root or rootless, additional configuration directories are consulted to allow for system-wide defaults and per-user overrides:

- For both rootful and rootless:
  - `/usr/share/containers/certs.d/`
  - `/etc/containers/certs.d/`
  - `/etc/docker/certs.d/`
- For rootful (UID == 0):
  - `/usr/share/containers/certs.rootful.d/`
  - `/etc/containers/certs.rootful.d/`
- For rootless (UID > 0):
  - `/usr/share/containers/certs.rootless.d/`
  - `/usr/share/containers/certs.rootless.d/<UID>/`
  - `/etc/containers/certs.rootless.d/`
  - `/etc/containers/certs.rootless.d/<UID>/`
- For per-user configuration:
  - `$XDG_CONFIG_HOME/containers/certs.d/` (or `$HOME/.config/containers/certs.d/` if `XDG_CONFIG_HOME` is unset)

If a given `host`[`:port`] directory exists in multiple locations, the effective configuration is determined by the unified configfile search order: user configuration takes precedence over `/etc`, which in turn takes precedence over `/usr/share`.

The port part presence / absence must precisely match the port usage in image references,
e.g. to affect `podman pull registry.example/foo`,
use a directory named `registry.example`, not `registry.example:443`.
`registry.example:443` would affect `podman pull registry.example:443/foo`.

## Directory Structure
A certs directory can contain one or more files with the following extensions:

* `*.crt`  files with this extensions will be interpreted as CA certificates
* `*.cert` files with this extensions will be interpreted as client certificates
* `*.key`  files with this extensions will be interpreted as client keys

Note that the client certificate-key pair will be selected by the file name (e.g., `client.{cert,key}`).
An exemplary setup for a registry running at `my-registry.com:5000` may look as follows:
```
/etc/containers/certs.d/    <- Certificate directory
└── my-registry.com:5000    <- Hostname[:port]
   ├── client.cert          <- Client certificate
   ├── client.key           <- Client key
   └── ca.crt               <- Certificate authority that signed the registry certificate
```

# HISTORY
Feb 2019, Originally compiled by Valentin Rothberg <rothberg@redhat.com>
