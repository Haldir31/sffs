#!/bin/sh
# Démonte un éventuel montage FUSE mort laissé par un arrêt brutal, puis lance sffs.
# Seul un montage FUSE est démonté : le dossier partagé par Docker (bind rshared) doit rester,
# sinon le montage de sffs ne remonterait pas vers l'hôte.
M="${SFFS_MOUNT:-/mnt/sffs}"
# Plusieurs montages morts peuvent s'empiler (redémarrages successifs) : tout démonter.
for _ in 1 2 3 4 5; do
  awk -v m="$M" '$5 == m && $0 ~ / - fuse/ { found = 1 } END { exit !found }' /proc/self/mountinfo || break
  fusermount3 -uz "$M" 2>/dev/null || umount -l "$M" 2>/dev/null || break
done
mkdir -p "$M"
exec /usr/local/bin/sffs "$@"
