#!/bin/bash
# Docker Compose : après la création du compte admin Silo, crée la clé d'API de sffs,
# l'écrit dans docker/.env et redémarre sffs.
set -euo pipefail
cd "$(dirname "$0")"
docker exec silo-postgres psql -U silo -d silo -qAtc "select 1 from users where role = 'admin' and enabled" | grep -q 1 || {
  echo "Crée d'abord le compte admin dans l'interface web de Silo." >&2; exit 1; }
KEY="sa_$(openssl rand -hex 32)"
HASH=$(printf %s "$KEY" | sha256sum | cut -d' ' -f1)
docker exec silo-postgres psql -U silo -d silo -qAtc \
  "insert into api_keys(user_id,label,key_hash,key_prefix,scopes) select (select id from users where role = 'admin' and enabled order by id limit 1),'sffs','$HASH','${KEY:0:11}','{}'"
sed -i "s|^SFFS_SILO_KEY=.*|SFFS_SILO_KEY=$KEY|" .env
docker compose -f compose.yml up -d sffs
echo "Clé enregistrée dans docker/.env, sffs redémarré."
