#!/bin/bash
# Construit les images de la pile, avec Docker ou Podman.
#
#   ./build-images.sh [sffs|silo|all]      (défaut : all)
#
# Variables (toutes facultatives) :
#   CONTAINER_ENGINE  docker ou podman (défaut : docker s'il est installé, sinon podman)
#   SFFS_IMAGE        défaut localhost/sffs:latest
#   SILO_IMAGE        défaut localhost/silo-sffs:latest
#   SILO_SRC          clone de travail de Silo (défaut ~/.cache/sffs-build/silo-server)
#   SILO_BASE         commit amont sur lequel appliquer les patchs (défaut : silo/BASE_COMMIT, testé)
#   IMAGE_SOURCE      URL du dépôt, ajoutée en étiquette org.opencontainers.image.source (publication)
#
# Silo : clone officiel -> commit SILO_BASE -> patchs silo/patches/*.patch -> image.
# Compter 5 à 15 min pour Silo, 1 à 2 min pour sffs.
set -euo pipefail
REPO=$(cd "$(dirname "$0")" && pwd)
WHAT=${1:-all}
ENGINE=${CONTAINER_ENGINE:-$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)}
SFFS_IMAGE=${SFFS_IMAGE:-localhost/sffs:latest}
SILO_IMAGE=${SILO_IMAGE:-localhost/silo-sffs:latest}
SILO_SRC=${SILO_SRC:-$HOME/.cache/sffs-build/silo-server}
SILO_BASE=${SILO_BASE:-$(cat "$REPO/silo/BASE_COMMIT")}
# Podman rootless : TMPDIR sur le disque de l'utilisateur (évite des erreurs crun sur certains hôtes).
[ "$ENGINE" = podman ] && export TMPDIR=${TMPDIR:-$HOME/.tmp} && mkdir -p "$TMPDIR"
# Docker : BuildKit nécessaire (RUN --mount, RUN --network du Dockerfile de Silo).
[ "$ENGINE" = docker ] && export DOCKER_BUILDKIT=1

# Empreinte git du dossier source (vide hors dépôt git) : SSDV2 ne reconstruit une image que si
# elle a changé.
tree() { git -C "$REPO" rev-parse "HEAD:$1" 2>/dev/null || true; }
src_label=()
[ -n "${IMAGE_SOURCE:-}" ] && src_label=(--label "org.opencontainers.image.source=$IMAGE_SOURCE")

build_sffs() {
  echo "== sffs -> $SFFS_IMAGE ($ENGINE)"
  "$ENGINE" build "${src_label[@]}" --label sffs.tree="$(tree sffs)" -t "$SFFS_IMAGE" "$REPO/sffs"
}

build_silo() {
  echo "== Silo $SILO_BASE + patchs -> $SILO_IMAGE ($ENGINE)"
  if [ ! -d "$SILO_SRC/.git" ]; then
    mkdir -p "$(dirname "$SILO_SRC")"
    git clone https://github.com/Silo-Server/silo-server.git "$SILO_SRC"
  fi
  cd "$SILO_SRC"
  git fetch -q origin
  # Branche reconstruite à chaque fois depuis l'amont : les patchs sont la seule source de vérité.
  git am --abort >/dev/null 2>&1 || true
  git checkout -q -f -B sffs "$SILO_BASE"
  if ! git -c user.name=sffs -c user.email=sffs@localhost am -q "$REPO"/silo/patches/*.patch; then
    git am --abort || true
    echo "Les patchs ne s'appliquent pas sur $SILO_BASE (garder silo/BASE_COMMIT, ou adapter les patchs)." >&2
    exit 1
  fi
  # Fichier d'exclusion du contexte de build propre à cette image (remis à zéro au prochain checkout -f).
  cp "$REPO/silo/dockerignore.sffs" .dockerignore
  # Dernière migration de base connue : l'installation SSDV2 refuse une base déjà migrée plus loin.
  mig=$(ls migrations/sql | sed -n 's/^\([0-9]\{14\}\)_.*\.sql$/\1/p' | sort | tail -1)
  "$ENGINE" build -f "$REPO/silo/Dockerfile.sffs" \
    "${src_label[@]}" --label sffs.silo.base="$SILO_BASE" --label sffs.silo.migration="$mig" --label sffs.tree="$(tree silo)" \
    --build-arg BUILD_REVISION="$(git rev-parse --short HEAD)" \
    --build-arg BUILD_DATE="$(date -u +%FT%TZ)" \
    -t "$SILO_IMAGE" .
}

case "$WHAT" in
  sffs) build_sffs ;;
  silo) build_silo ;;
  all) build_sffs; build_silo ;;
  *) echo "usage: $0 [sffs|silo|all]" >&2; exit 2 ;;
esac
echo "terminé : $WHAT"
