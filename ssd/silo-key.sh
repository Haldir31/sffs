#!/bin/bash
# Après la création du compte admin Silo : crée une clé d'API admin pour sffs (scans ciblés
# des dossiers modifiés), l'enregistre dans les secrets sffs et redéploie sffs.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
SECRETS="$SETTINGS_STORAGE/docker/$USER/sffs-secrets"
[ -d "$SECRETS" ] || { echo "sffs n'est pas installé ($SECRETS absent)" >&2; exit 1; }
docker exec db-silosffs psql -U silo -d silo -qAtc "select 1 from users where id = 1" | grep -q 1 || {
  echo "Crée d'abord le compte admin dans l'interface web de Silo." >&2; exit 1; }
KEY="sa_$(openssl rand -hex 32)"
HASH=$(printf %s "$KEY" | sha256sum | cut -d' ' -f1)
docker exec db-silosffs psql -U silo -d silo -qAtc \
  "insert into api_keys(user_id,label,key_hash,key_prefix,scopes) values (1,'sffs','$HASH','${KEY:0:11}','{}')"
umask 077; printf '%s\n' "$KEY" > "$SECRETS/SFFS_SILO_KEY"
echo "Clé créée. Redéploiement de sffs…"
ssd_launch sffs
