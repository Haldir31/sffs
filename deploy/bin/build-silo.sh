#!/bin/bash
# Construit l'image Silo modifiée : Silo officiel au commit silo/BASE_COMMIT + patchs silo/patches.
# Usage : build-silo.sh [commit-ou-branche-amont]   (défaut : silo/BASE_COMMIT, testé)
# Durée : 5 à 15 min selon la machine.
. "$(dirname "$0")/common.sh"
SRC=$STACK/src/silo-server
BASE=${1:-$(cat "$REPO/silo/BASE_COMMIT")}

if [ ! -d "$SRC/.git" ]; then
  mkdir -p "$STACK/src"
  git clone https://github.com/Silo-Server/silo-server.git "$SRC" || exit 1
fi
cd "$SRC"
git fetch -q origin || exit 1
# Branche reconstruite à chaque fois depuis l'amont : les patchs sont la seule source de vérité.
git checkout -q -B sffs "$BASE" || exit 1
git -c user.name=sffs -c user.email=sffs@localhost am -q --committer-date-is-author-date "$REPO"/silo/patches/*.patch || {
  echo "Les patchs ne s'appliquent pas sur $BASE. Corriger puis 'git am --continue', ou revenir au commit de silo/BASE_COMMIT." >&2
  exit 1
}
echo "Silo $(git rev-parse --short "$BASE") + $(ls "$REPO"/silo/patches/*.patch | wc -l) patchs"

cp "$REPO/silo/dockerignore.sffs" .dockerignore.sffs
podman build -f "$REPO/silo/Dockerfile.sffs" --ignorefile .dockerignore.sffs \
  --build-arg BUILD_REVISION="$(git rev-parse --short HEAD)" --build-arg BUILD_DATE="$(date -u +%FT%TZ)" \
  -t "$SILO_IMAGE" . || exit 1
echo "image prête : $SILO_IMAGE (appliquée par redeploy.sh)"
