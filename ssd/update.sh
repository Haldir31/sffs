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
for app in "${apps[@]}"; do
  case $app in
    sffs) CONTAINER_ENGINE=docker SFFS_IMAGE=localhost/sffs:latest "$REPO/build-images.sh" sffs ;;
    silosffs) CONTAINER_ENGINE=docker SILO_IMAGE=localhost/silo-sffs:latest \
                SILO_SRC="$SETTINGS_STORAGE/docker/$USER/silosffs/src" "$REPO/build-images.sh" silo ;;
  esac
  ssd_launch "$app"
done
