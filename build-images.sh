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

build_sffs() {
  echo "== sffs -> $SFFS_IMAGE ($ENGINE)"
  "$ENGINE" build -t "$SFFS_IMAGE" "$REPO/sffs"
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
  "$ENGINE" build -f "$REPO/silo/Dockerfile.sffs" \
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
