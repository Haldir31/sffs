#!/bin/bash
# Après la création du compte admin Silo : crée une clé d'API admin pour sffs (scans ciblés
# des dossiers modifiés), l'enregistre dans les secrets sffs et redéploie sffs
# (après la fin des scans Silo en cours ; lancer sous nohup si besoin).
set -euo pipefail
. "$(dirname "$0")/lib.sh"
SECRETS="$SETTINGS_STORAGE/docker/$USER/sffs-secrets"
[ -d "$SECRETS" ] || { echo "sffs n'est pas installé ($SECRETS absent)" >&2; exit 1; }
docker exec db-silosffs psql -U silo -d silo -qAtc "select 1 from users where role = 'admin' and enabled" | grep -q 1 || {
  echo "Crée d'abord le compte admin dans l'interface web de Silo." >&2; exit 1; }
KEY="sa_$(openssl rand -hex 32)"
HASH=$(printf %s "$KEY" | sha256sum | cut -d' ' -f1)
docker exec db-silosffs psql -U silo -d silo -qAtc \
  "insert into api_keys(user_id,label,key_hash,key_prefix,scopes) select (select id from users where role = 'admin' and enabled order by id limit 1),'sffs','$HASH','${KEY:0:11}','{}'"
umask 077; printf '%s\n' "$KEY" > "$SECRETS/SFFS_SILO_KEY"
# Redéployer sffs démonte /mnt/sffs une minute : on attend la fin des scans Silo en cours
# (le premier scan complet prend plusieurs heures), sinon Silo verrait des fichiers disparaître.
active() { docker exec db-silosffs psql -U silo -d silo -qAtc \
  "select count(*) from scan_runs where status in ('accepted','running')"; }
if [ "$(active)" != 0 ]; then
  echo "Clé créée. Scans Silo en cours : sffs sera redéployé à leur fin (Ctrl-C pour annuler ;"
  echo "relancer plus tard avec : $(dirname "$0")/update.sh sffs)."
  while [ "$(active)" != 0 ]; do sleep 300; done
fi
echo "Redéploiement de sffs…"
ssd_launch sffs
