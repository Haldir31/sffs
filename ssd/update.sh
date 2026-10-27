#!/bin/bash
# Met à jour sffs et/ou Silo après un "git pull" du dépôt sffs : reconstruit l'image puis
# redéploie l'application par SSDV2 (secrets et données conservés).
#
#   ssd/update.sh [sffs|silo|all]     (défaut : all)
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(dirname "$HERE")
. "$HERE/lib.sh"
"$HERE/install.sh" --files >/dev/null
case "${1:-all}" in
  sffs) apps=(sffs) ;;
  silo) apps=(silosffs) ;;
  all) apps=(sffs silosffs) ;;
  *) echo "usage: $0 [sffs|silo|all]" >&2; exit 2 ;;
esac
# Migration (octobre 2026) : sffs montait directement /mnt/sffs ; il monte maintenant /mnt/sffs/sf
# et Silo monte /mnt/sffs en entier, pour voir les nouveaux montages sans redémarrer.
if [ "$(findmnt -n -o FSTYPE --mountpoint /mnt/sffs 2>/dev/null)" = fuse.sffs ]; then
  echo "Migration du montage : /mnt/sffs -> /mnt/sffs/sf (Silo sera redéployé)"
  docker stop sffs >/dev/null 2>&1 || true
  while mountpoint -q /mnt/sffs; do sudo umount -l /mnt/sffs; done
  [[ " ${apps[*]} " == *" silosffs "* ]] || apps+=(silosffs)
  SILO_NO_BUILD=1
fi
for app in "${apps[@]}"; do
  case $app in
    sffs) CONTAINER_ENGINE=docker SFFS_IMAGE=localhost/sffs:latest "$REPO/build-images.sh" sffs ;;
    silosffs) [ -n "${SILO_NO_BUILD:-}" ] && [ "${1:-all}" = sffs ] && { ssd_launch "$app"; continue; }
              CONTAINER_ENGINE=docker SILO_IMAGE=localhost/silo-sffs:latest \
                SILO_SRC="$SETTINGS_STORAGE/docker/$USER/silosffs/src" "$REPO/build-images.sh" silo ;;
  esac
  ssd_launch "$app"
done
