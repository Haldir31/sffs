#!/bin/bash
# Intègre sffs et Silo (silosffs) dans une installation SSDV2 existante, puis les installe
# par le mécanisme SSDV2 standard (launch_service : pretasks -> conteneur -> posttasks -> registre).
#
#   ssd/install.sh [options]          installe sffs puis silosffs
#   ssd/install.sh --files            copie seulement les définitions d'applications
#
# Options (toutes facultatives ; sans elles, les questions sont posées pendant l'installation) :
#   --manifest <url>      URL d'addon StreamFusion existante (…/manifest.json) ; sans elle, la
#                         configuration est générée (clé API StreamFusion dédiée + clé AllDebrid de decypharr)
#   --subdomain <nom>     sous-domaine de Silo (défaut demandé ; l'API Jellyfin est sur <nom>-jf)
#   --auth <type>         authentification Traefik de Silo : aucune conseillé (Silo a ses comptes)
#   --only sffs|silo      n'installer qu'une des deux applications
#
# La clé TMDB est reprise de StreamFusion. Prérequis : StreamFusion installé par SSDV2.
# Ne modifie aucun fichier suivi par git dans SSDV2 : les mises à jour (git pull) restent possibles.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(dirname "$HERE")
. "$HERE/lib.sh"

FILES_ONLY=0 MANIFEST="" SUB="" AUTH="" ONLY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --files) FILES_ONLY=1 ;;
    --manifest) MANIFEST=$2; shift ;;
    --subdomain) SUB=$2; shift ;;
    --auth) AUTH=$2; shift ;;
    --only) ONLY=$2; shift ;;
    *) echo "option inconnue : $1" >&2; exit 2 ;;
  esac
  shift
done

ok()   { printf '  \033[32mOK\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mERREUR\033[0m %s\n' "$*" >&2; exit 1; }

echo "== Vérifications"
command -v docker >/dev/null || fail "docker introuvable"
docker info >/dev/null 2>&1 || fail "docker inaccessible pour $USER (groupe docker ?)"
ok "docker"
[ -e /dev/fuse ] || fail "/dev/fuse absent (modprobe fuse)"
ok "/dev/fuse"
running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]; }
running stremio-postgres || fail "conteneur stremio-postgres arrêté ou absent : installer StreamFusion avec SSDV2 d'abord"
SF=""
for c in $(docker ps --filter label=ssdv2.app=streamfusion --format '{{.Names}}') stream-fusion streamfusion; do
  case "$c" in stremio-*|taskiq-*|sftorznab*) continue ;; esac
  running "$c" && docker inspect -f '{{.Config.Image}}' "$c" | grep -q stream-fusion && { SF=$c; break; }
done
[ -n "$SF" ] || fail "conteneur StreamFusion introuvable (stream-fusion ou streamfusion)"
ok "StreamFusion : conteneurs $SF et stremio-postgres"
prop=$(findmnt -no PROPAGATION --target /mnt 2>/dev/null || true)
case "$prop" in
  shared*) ok "/mnt en propagation partagée" ;;
  *) echo "  /mnt n'est pas en propagation partagée ($prop) : le montage sffs ne serait pas visible par Silo."
     echo "  Correctif : sudo mount --make-rshared /   (à rendre permanent, ex. via systemd)"; exit 1 ;;
esac

echo "== Définitions d'applications"
mkdir -p "$SETTINGS_STORAGE/vars"
for app in sffs silosffs; do
  sed -e "s|__SFFS_REPO__|$REPO|" -e "s|__SF_CONTAINER__|$SF|" "$HERE/vars/$app.yml" > "$SETTINGS_STORAGE/vars/$app.yml"
  ok "vars/$app.yml -> $SETTINGS_STORAGE/vars/"
done
# Les pretasks/posttasks ne sont cherchées que dans les sources SSDV2 : fichiers non suivis,
# exclus de git pour ne pas gêner les mises à jour.
EXCL="$SETTINGS_SOURCE/.git/info/exclude"
for f in pretasks/sffs.yml pretasks/silosffs.yml posttasks/silosffs.yml; do
  install -m 0644 "$HERE/$f" "$SETTINGS_SOURCE/includes/dockerapps/$f"
  if [ -f "$EXCL" ] && ! grep -qxF "includes/dockerapps/$f" "$EXCL"; then echo "includes/dockerapps/$f" >> "$EXCL"; fi
  ok "$f -> $SETTINGS_SOURCE/includes/dockerapps/"
done
chmod +x "$REPO/build-images.sh" "$REPO/deploy/bin/sf-config.sh" "$HERE"/*.sh

[ $FILES_ONLY = 1 ] && { echo "Définitions en place."; exit 0; }

# Valeurs passées aux pretasks sans question (fichier temporaire lu par launch_service).
EXTRA=$(mktemp); chmod 600 "$EXTRA"; trap 'rm -f "$EXTRA"' EXIT
TMDB=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$SF" | sed -n 's/^TMDB_API_KEY=//p')
{
  [ -n "$TMDB" ] && printf 'streamfusion_tmdb_api_key: "%s"\n' "$TMDB"
  [ -n "$MANIFEST" ] && printf 'sffs_manifest_url: "%s"\n' "$MANIFEST"
  true
} > "$EXTRA"
export ANSIBLE_EXTRA_VARS_FILE=$EXTRA
[ -n "$TMDB" ] && ok "clé TMDB reprise de StreamFusion"

if [ "$ONLY" != silo ]; then
  echo "== Installation de sffs"
  ssd_launch sffs || fail "installation de sffs"
  echo "   Chargement du catalogue (1 à 5 min ; 15 à 30 min au premier démarrage, le temps de remplir le cache TMDB)…"
  for _ in $(seq 1 240); do [ -d /mnt/sffs/sf/films ] && break; sleep 10; done
  [ -d /mnt/sffs/sf/films ] || fail "/mnt/sffs/sf non monté : docker logs sffs"
  ok "/mnt/sffs/sf monté ($(ls /mnt/sffs/sf/films | wc -l) films)"
fi

if [ "$ONLY" != sffs ]; then
  echo "== Installation de Silo (silosffs)"
  [ -n "$SUB" ] && ssd_account "sub.silosffs.silosffs" "$SUB"
  [ -n "$AUTH" ] && ssd_account "sub.silosffs.auth" "$AUTH"
  echo "   Première fois : construction de l'image Silo, 5 à 15 min (plus sur ARM ou petit CPU)."
  ssd_launch silosffs || fail "installation de silosffs"
fi

cat <<EOF

Terminé. Dernières étapes :
  1. Ouvre Silo, crée le compte admin.
  2. Ajoute les bibliothèques : Films -> /mnt/sf/films, Séries -> /mnt/sf/series
  3. $HERE/silo-key.sh      (scans ciblés des nouveautés StreamFusion)
EOF
