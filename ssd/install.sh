#!/bin/bash
# Intègre sffs et Silo (silosffs) dans une installation SSDV2 existante, puis les installe
# par le mécanisme SSDV2 standard (launch_service : pretasks -> conteneur -> posttasks -> registre).
#
#   ssd/install.sh            installe (ou met à jour la définition de) sffs puis silosffs
#   ssd/install.sh --files    copie seulement les fichiers d'application, sans rien lancer
#
# Prérequis : StreamFusion installé par SSDV2 (conteneurs stream-fusion et stremio-postgres).
# Ne modifie aucun fichier suivi par git dans SSDV2 : les mises à jour (git pull) restent possibles.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(dirname "$HERE")
. "$HERE/lib.sh"

ok()   { printf '  \033[32mOK\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mERREUR\033[0m %s\n' "$*" >&2; exit 1; }

echo "== Vérifications"
command -v docker >/dev/null || fail "docker introuvable"
docker info >/dev/null 2>&1 || fail "docker inaccessible pour $USER (groupe docker ?)"
ok "docker"
[ -e /dev/fuse ] || fail "/dev/fuse absent (modprobe fuse)"
ok "/dev/fuse"
for c in stremio-postgres stream-fusion; do
  [ "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" = true ] || fail "conteneur $c arrêté ou absent : installer StreamFusion avec SSDV2 d'abord"
done
ok "StreamFusion (stream-fusion, stremio-postgres)"
prop=$(findmnt -no PROPAGATION --target /mnt 2>/dev/null || true)
case "$prop" in
  shared*) ok "/mnt en propagation partagée" ;;
  *) echo "  /mnt n'est pas en propagation partagée ($prop) : le montage sffs ne serait pas visible par Silo."
     echo "  Correctif : sudo mount --make-rshared /   (à rendre permanent, ex. via systemd)"; exit 1 ;;
esac

echo "== Fichiers d'application"
mkdir -p "$SETTINGS_STORAGE/vars"
for app in sffs silosffs; do
  sed "s|__SFFS_REPO__|$REPO|" "$HERE/vars/$app.yml" > "$SETTINGS_STORAGE/vars/$app.yml"
  ok "vars/$app.yml -> $SETTINGS_STORAGE/vars/"
done
# Les pretasks/posttasks ne sont cherchées que dans les sources SSDV2 : fichiers non suivis,
# exclus de git pour ne pas gêner les mises à jour.
EXCL="$SETTINGS_SOURCE/.git/info/exclude"
for f in pretasks/sffs.yml pretasks/silosffs.yml posttasks/silosffs.yml; do
  install -m 0644 "$HERE/$f" "$SETTINGS_SOURCE/includes/dockerapps/$f"
  [ -f "$EXCL" ] && ! grep -qxF "includes/dockerapps/$f" "$EXCL" && echo "includes/dockerapps/$f" >> "$EXCL"
  ok "$f -> $SETTINGS_SOURCE/includes/dockerapps/"
done
chmod +x "$REPO/build-images.sh" "$REPO/deploy/bin/sf-config.sh" "$HERE"/*.sh

[ "${1:-}" = --files ] && { echo "Fichiers en place. Installation : $0"; exit 0; }

echo "== Installation de sffs (questions : URL du manifest StreamFusion, clé TMDB)"
ssd_launch sffs || fail "installation de sffs"
echo "   Chargement du catalogue (1 à 5 min, plus au premier démarrage)…"
for _ in $(seq 1 120); do mountpoint -q /mnt/sffs && [ -d /mnt/sffs/films ] && break; sleep 10; done
mountpoint -q /mnt/sffs || fail "/mnt/sffs non monté : docker logs sffs"
ok "/mnt/sffs monté ($(ls /mnt/sffs/films | wc -l) films)"

echo "== Installation de Silo (questions : sous-domaine, authentification ; choisir 'aucune', Silo a la sienne)"
echo "   Première fois : construction de l'image, 5 à 15 min."
ssd_launch silosffs || fail "installation de silosffs"

cat <<EOF

Terminé. Dernières étapes :
  1. Ouvre Silo, crée le compte admin.
  2. Ajoute les bibliothèques : Films -> /mnt/sf/films, Séries -> /mnt/sf/series
  3. $HERE/silo-key.sh      (scans ciblés des nouveautés StreamFusion)
EOF
