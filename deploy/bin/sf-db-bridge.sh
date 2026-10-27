#!/bin/bash
# Pont en lecture seule vers le Postgres de StreamFusion quand celui-ci tourne dans un pod
# podman de ce serveur sans port publié (SF_DB_BRIDGE=1 dans stack.env) :
#   socket Unix $SFFS_STACK/run/sfpg/.s.PGSQL.5432  ->  127.0.0.1:5432 dans le réseau du pod.
# Ne modifie rien côté StreamFusion. Nécessite socat. Relancé par start.sh si besoin.
. "$(dirname "$0")/common.sh"
PIDF=$RUN/sf-db-bridge.pid
mkdir -p "$RUN/sfpg"; chmod 700 "$RUN/sfpg"
if [ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null; then echo "pont déjà lancé ($(cat "$PIDF"))"; exit 0; fi
INF=$(podman pod inspect "$SF_POD" --format '{{.InfraContainerID}}') || { echo "pod $SF_POD introuvable" >&2; exit 1; }
NS=$(podman inspect "$INF" --format '{{.NetworkSettings.SandboxKey}}')
if [ -n "$NS" ]; then ENTER=(podman unshare nsenter --net="$NS")
else C=$(pgrep -f "conmon.*-c $INF" | head -1); P=$(pgrep -P "$C" | head -1)
  [ -n "$P" ] || exit 1; ENTER=(nsenter -t "$P" -U -n --preserve-credentials); fi
rm -f "$RUN/sfpg/.s.PGSQL.5432"
setsid nohup "${ENTER[@]}" socat "UNIX-LISTEN:$RUN/sfpg/.s.PGSQL.5432,fork,mode=600" TCP:127.0.0.1:5432 \
  >>"$RUN/sf-db-bridge.log" 2>&1 </dev/null &
sleep 1
pgrep -f "UNIX-LISTEN:$RUN/sfpg/.s.PGSQL.543[2]" | head -1 > "$PIDF"
echo "pont lancé (pid $(cat "$PIDF"))"
