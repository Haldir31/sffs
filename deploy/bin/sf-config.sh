#!/bin/bash
# Déduit SF_STREAM_CFG et SF_PLAYBACK_CFG de l'URL d'installation de l'addon StreamFusion.
# Usage : sf-config.sh 'https://streamfusion.example.org/<config>/manifest.json' [imdb-de-test]
# Le film de test (défaut : tt0172495, Gladiator) doit avoir au moins une source.
set -u
M=${1:?usage: sf-config.sh <url-du-manifest> [imdb-de-test]}
IMDB=${2:-tt0172495}
BASE=${M%/*/manifest.json}
CFG=${M%/manifest.json}; CFG=${CFG##*/}
URL=$(curl -s -m 60 "$BASE/$CFG/stream/movie/$IMDB.json" | grep -o '"url": *"[^"]*/playback/[^"]*"' | head -1 | sed 's/.*"\(http[^"]*\)"/\1/')
[ -n "$URL" ] || { echo "aucun flux /playback/ pour $IMDB : vérifier l'URL, le débrideur, ou essayer un autre film" >&2; exit 1; }
PB=${URL#*/playback/}; PB=${PB%%/*}
echo "SF_STREAM_CFG=$CFG"
echo "SF_PLAYBACK_CFG=$PB"
echo "# SF_BASE : l'adresse de StreamFusion vue depuis ce serveur (publique : $BASE)"
