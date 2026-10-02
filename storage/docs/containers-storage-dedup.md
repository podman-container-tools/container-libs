# containers-storage-dedup 1 "November 2024"

## NAME
containers-storage-dedup - Deduplicate similar files in the images

## SYNOPSIS
**containers-storage** **dedup**

## DESCRIPTION
Find similar files in the images and deduplicate them. It requires reflink support from the file system.

To check if your filesystem supports reflinks, run **containers-storage status** and look for the **Supports reflinks** field. Deduplication will only work on filesystems that report reflink support (Btrfs, XFS with reflink=1, OCFS2).

## OPTIONS
**--hash-method** *method*

Specify the function to use to calculate the hash for a file.  It can be one of: *size*, *crc*, *sha256sum*.

## EXAMPLE

    containers-storage dedup

## SEE ALSO
containers-storage-status(1)
