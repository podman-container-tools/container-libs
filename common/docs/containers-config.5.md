% containers-config 5

# NAME

containers-config - Rules for loading container engine configuration files

# DESCRIPTION

This document describes the loading order of the **[containers.conf(5)](containers.conf.5.md)**,
**[containers-storage.conf(5)](../../storage/docs/containers-storage.conf.5.md)** and
**[containers-registries.conf(5)](../../image/docs/containers-registries.conf.5.md)**.
Refer to their individual man pages for more details on the file formats.

# Search Locations

In the following paths `<name>` is a placeholder for either `containers`, `storage`, or `registries`
depending on the specific config file.

The paths are listed from lowest to highest precedence (i.e. later paths override / are preferred
over the earlier paths, see below for details).

## Linux and macOS

System defaults:

- `/usr/share/containers/<name>.conf`
- `/usr/share/containers/<name>.conf.d/`
- `/usr/share/containers/<name>.rootful.conf.d/` (only when UID == 0)
- `/usr/share/containers/<name>.rootless.conf.d/` (only when UID > 0)
- `/usr/share/containers/<name>.rootless.conf.d/<UID>/` (only when UID > 0)

Admin overrides:

- `/etc/containers/<name>.conf`
- `/etc/containers/<name>.conf.d/`
- `/etc/containers/<name>.rootful.conf.d/` (only when UID == 0)
- `/etc/containers/<name>.rootless.conf.d/` (only when UID > 0)
- `/etc/containers/<name>.rootless.conf.d/<UID>/` (only when UID > 0)

User overrides:

- `$XDG_CONFIG_HOME/containers/<name>.conf` or if `$XDG_CONFIG_HOME` is unset then `$HOME/.config/containers/<name>.conf`
- `$XDG_CONFIG_HOME/containers/<name>.conf.d/` or if `$XDG_CONFIG_HOME` is unset then `$HOME/.config/containers/<name>.conf.d/`

## FreeBSD

System defaults:

- `/usr/local/share/containers/<name>.conf`
- `/usr/local/share/containers/<name>.conf.d/`
- `/usr/local/share/containers/<name>.rootful.conf.d/` (only when UID == 0)
- `/usr/local/share/containers/<name>.rootless.conf.d/` (only when UID > 0)
- `/usr/local/share/containers/<name>.rootless.conf.d/<UID>/` (only when UID > 0)

Admin overrides:

- `/usr/local/etc/containers/<name>.conf`
- `/usr/local/etc/containers/<name>.conf.d/`
- `/usr/local/etc/containers/<name>.rootful.conf.d/` (only when UID == 0)
- `/usr/local/etc/containers/<name>.rootless.conf.d/` (only when UID > 0)
- `/usr/local/etc/containers/<name>.rootless.conf.d/<UID>/` (only when UID > 0)

User overrides:

- `$XDG_CONFIG_HOME/containers/<name>.conf` or if `$XDG_CONFIG_HOME` is unset then `$HOME/.config/containers/<name>.conf`
- `$XDG_CONFIG_HOME/containers/<name>.conf.d/` or if `$XDG_CONFIG_HOME` is unset then `$HOME/.config/containers/<name>.conf.d/`

## Windows

There is no location for system defaults.

Admin overrides:

- `%ProgramData%\containers\<name>.conf`
- `%ProgramData%\containers\<name>.conf.d\`

User overrides:

- `%APPDATA%\containers\<name>.conf`
- `%APPDATA%\containers\<name>.conf.d\`


# Load Order

The search locations are split into two file categories, the main files and the drop-in directories.

Only one main file, `<name>.conf`, will be loaded: the highest-precedence file that exists.
If no `<name>.conf` file exists, no main file is loaded, which is valid too.

After the main file, drop-in directories are loaded. Each file can override settings
set in earlier files. Files within the directory must be suffixed with `.conf`, otherwise they get ignored.

All drop-in files will be sorted in the lexicographic order of the file name, if there are two or more
drop-in files with the same file name only the one with the highest precedence will be used and thus
replaces the prior one.

# EXAMPLES

## Example 1

- ~~`/usr/share/containers/containers.conf`~~ (overridden by `$XDG_CONFIG_HOME/containers/containers.conf` and thus not read)
- `$XDG_CONFIG_HOME/containers/containers.conf`
- `/usr/share/containers/containers.conf.d/00-vendor.conf`
- ~~`/etc/containers/containers.conf.d/50-myconf.conf`~~ (overridden by `$XDG_CONFIG_HOME/containers/containers.conf.d/50-myconf.conf` and thus not read)
- `$XDG_CONFIG_HOME/containers/containers.conf.d/50-myconf.conf`
- `/usr/share/containers/containers.conf.d/99-conf.conf`

## Example 2

Consider the following files with their contents:

~~`/usr/share/containers/containers.conf`~~ (overridden by `/etc/containers/containers.conf`):
```
field_1 = a
```

`/etc/containers/containers.conf`:
```
field_2 = b
```

~~`/usr/share/containers/containers.conf.d/10-vendor.conf`~~ (overridden by `$XDG_CONFIG_HOME/containers/containers.conf.d/10-vendor.conf`):
```
field_3 = c
```

`/usr/share/containers/containers.conf.d/99-important.conf`:
```
field_4 = d
```

`/usr/share/containers/containers.rootless.conf.d/50-my.conf`:
```
field_5 = e
```

`$XDG_CONFIG_HOME/containers/containers.conf.d/10-vendor.conf`:
```
# empty
```

`$XDG_CONFIG_HOME/containers/containers.conf.d/33-opt.conf` (this is read but field_4 is overridden by `/usr/share/containers/containers.conf.d/99-important.conf` as `99-important.conf` is sorted later):
```
field_4 = user
field_6 = f
```

Now parsing this as user with UID 1000 results in this final config:

```
field_2 = b
field_4 = d
field_5 = e
field_6 = f
```


# SEE ALSO

**[containers.conf(5)](containers.conf.5.md)**,
**[containers-storage.conf(5)](../../storage/docs/containers-storage.conf.5.md)**,
**[containers-registries.conf(5)](../../image/docs/containers-registries.conf.5.md)**
