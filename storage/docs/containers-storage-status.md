# containers-storage-status 1 "August 2016"

## NAME
containers-storage-status - Output status information from the storage library's driver

## SYNOPSIS
**containers-storage** **status**

## DESCRIPTION
Queries the storage library's driver for status information.

The status output includes a **Supports reflinks** field which indicates whether the filesystem supports reflink/CoW (copy-on-write) file cloning. Reflinks are supported on Btrfs, XFS (with reflink=1 mount option), and OCFS2. When reflinks are supported, storage deduplication will work efficiently.

## EXAMPLE

    containers-storage status

## SEE ALSO
containers-storage-version(1), containers-storage-dedup(1)
