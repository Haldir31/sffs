#!/bin/bash
# Génère l'URL d'addon StreamFusion (configuration chiffrée) sans passer par la page /configure,
# puis affiche SF_STREAM_CFG et SF_PLAYBACK_CFG pour sffs.
#
# Variables :
#   SF_URL        adresse de StreamFusion vue d'ici (ex. http://172.18.0.14:8080)      obligatoire
#   SF_PUBLIC     adresse publique de StreamFusion (addonHost, ex. https://streamfusion.example.org)
#                 défaut : SF_URL
#   SF_API_KEY    clé d'API StreamFusion (table api_keys)                              obligatoire
#   AD_TOKEN      clé API AllDebrid                                                     obligatoire
#   SF_LANGS      langues de recherche StreamFusion, séparées par des virgules          défaut : fr,multi
#   TEST_IMDB     film de test pour récupérer SF_PLAYBACK_CFG                          défaut : tt0172495
#
# Même chiffrement que l'interface : POST /api/config/encode avec le jeton CSRF de /configure.
set -euo pipefail
: "${SF_URL:?SF_URL manquant}" "${SF_API_KEY:?SF_API_KEY manquant}" "${AD_TOKEN:?AD_TOKEN manquant}"
SF_PUBLIC=${SF_PUBLIC:-$SF_URL}
export SF_PUBLIC SF_API_KEY AD_TOKEN SF_LANGS=${SF_LANGS:-fr,multi}

CSRF=$(curl -sf -m 30 "$SF_URL/configure" | sed -n 's/.*__CSRF_TOKEN__ *= *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$CSRF" ] || { echo "jeton CSRF introuvable sur $SF_URL/configure" >&2; exit 1; }

# Configuration équivalente à l'interface : AllDebrid seul, tri par qualité, sans indexeur privé.
BODY=$(python3 - <<'EOF'
import json, os
cfg = {
  "addonHost": os.environ["SF_PUBLIC"].rstrip("/"), "apiKey": os.environ["SF_API_KEY"],
  "service": ["AllDebrid"], "RDToken": "", "ADToken": os.environ["AD_TOKEN"], "TBToken": "", "PMToken": "",
  "TBUsenet": False, "TBSearch": False,
  "c411": False, "tr4ker": False, "yggreborn": False, "c411ApiKey": "", "c411Passkey": "",
  "tr4kerApiKey": "", "yggrebornApiKey": "", "generationfree": False, "generationfreeApiKey": "",
  "generationfreePasskey": "", "abn": False, "abnApiKey": "", "abnPasskey": "", "g3mini": False,
  "g3miniApiKey": "", "g3miniPasskey": "", "theoldschool": False, "theoldschoolApiKey": "",
  "theoldschoolPasskey": "", "nostradamus": False, "nostradamusApiKey": "", "nostradamusPasskey": "",
  "maxSize": 150, "exclusionKeywords": [],
  "languages": [l.strip() for l in os.environ["SF_LANGS"].split(",") if l.strip()],
  "sort": "quality", "scoringTemplateId": None, "resultsPerQuality": 10, "maxResults": 30,
  "minCachedResults": 10, "rdMinCachedBeforeCheck": 3, "minPostgresResults": 20, "postgresMaxAgeDays": 7,
  "exclusion": ["cam"], "zilean": True, "utopeer": False, "meiliCache": False,
  "meiliCachePriority": False, "utopeerPriority": False,
  "indexerCategories": {k: "disabled" for k in ["c411", "tr4ker", "yggreborn", "generationfree", "g3mini",
                        "theoldschool", "abn", "nostradamus", "utopeer", "jackett", "meiliCache"]} | {"zilean": "fallback_private"},
  "yggtorrentCtg": False, "utopeerCtg": False, "torrenting": False, "debrid": True,
  "metadataProvider": "tmdb", "debridDownloader": "AllDebrid",
  "stremthru": False, "stremthruUrl": "https://stremthru.13377001.xyz/",
  "DLToken": "", "EDToken": "", "OCCredentials": "", "PPCredentials": "",
  "jackett": False, "anonymizeMagnets": False,
}
print(json.dumps({"config": cfg}))
EOF
)
TOKEN=$(curl -sf -m 30 -X POST "$SF_URL/api/config/encode" -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $CSRF" --data-binary "$BODY" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["token"])')
[ -n "$TOKEN" ] || { echo "échec de /api/config/encode" >&2; exit 1; }

# Même sortie que sf-config.sh (SF_STREAM_CFG, SF_PLAYBACK_CFG), StreamFusion interrogé en local.
"$(dirname "$0")/sf-config.sh" "${SF_PUBLIC%/}/$TOKEN/manifest.json" "${TEST_IMDB:-tt0172495}" "$SF_URL"
echo "# URL d'addon Stremio : ${SF_PUBLIC%/}/$TOKEN/manifest.json"
