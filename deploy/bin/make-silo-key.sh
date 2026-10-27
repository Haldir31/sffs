#!/bin/bash
# Crée une clé d'API admin Silo pour sffs (scans ciblés) et l'affiche une seule fois.
# À reporter dans SFFS_SILO_KEY de sffs.env. Silo doit avoir démarré au moins une fois et
# un compte admin doit exister (la clé est rattachée au premier admin).
. "$(dirname "$0")/common.sh"
set -a; . "$CONF/pg.env"; set +a
KEY="sa_$(openssl rand -hex 32)"
HASH=$(printf %s "$KEY" | sha256sum | cut -d' ' -f1)
podman exec "$PG" psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -qAtc \
  "insert into api_keys(user_id,label,key_hash,key_prefix,scopes) select (select id from users where role = 'admin' and enabled order by id limit 1),'sffs','$HASH','${KEY:0:11}','{}'" || exit 1
echo "SFFS_SILO_KEY=$KEY"
