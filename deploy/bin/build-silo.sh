#!/bin/bash
# Construit l'image Silo modifiée pour la pile podman (voir build-images.sh à la racine du dépôt).
# Usage : build-silo.sh [commit-ou-branche-amont]   (défaut : silo/BASE_COMMIT, testé)
. "$(dirname "$0")/common.sh"
CONTAINER_ENGINE=podman SILO_IMAGE="$SILO_IMAGE" SILO_SRC="$STACK/src/silo-server" \
  SILO_BASE="${1:-$(cat "$REPO/silo/BASE_COMMIT")}" "$REPO/build-images.sh" silo || exit 1
echo "image prête : $SILO_IMAGE (appliquée par redeploy.sh)"
