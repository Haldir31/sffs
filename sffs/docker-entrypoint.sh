#!/bin/sh
# Démonte un éventuel montage FUSE mort laissé par un arrêt brutal, puis lance sffs.
# Seul un montage FUSE est démonté : le dossier partagé par Docker (bind rshared) doit rester,
# sinon le montage de sffs ne remonterait pas vers l'hôte.
M="${SFFS_MOUNT:-/mnt/sffs/sf}"
mkdir -p "$M"
if awk -v m="$M" '$5 == m && $0 ~ / - fuse/ { found = 1 } END { exit !found }' /proc/self/mountinfo; then
  fusermount3 -uz "$M" 2>/dev/null || umount -l "$M" 2>/dev/null || true
fi
exec /usr/local/bin/sffs "$@"
