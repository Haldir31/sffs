#!/bin/sh
# Démonte un éventuel montage mort laissé par un arrêt brutal, puis lance sffs.
fusermount3 -uz "${SFFS_MOUNT:-/mnt/sffs}" 2>/dev/null || umount -l "${SFFS_MOUNT:-/mnt/sffs}" 2>/dev/null || true
mkdir -p "${SFFS_MOUNT:-/mnt/sffs}"
exec /usr/local/bin/sffs "$@"
