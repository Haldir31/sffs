#!/bin/bash
# Démarre la pile dans l'ordre : pont StreamFusion (option) -> sffs -> pod -> Silo.
# Ne touche à rien de ce qui fonctionne : peut être relancé sans risque (cron, reboot).
. "$(dirname "$0")/common.sh"
MNT=$(sffs_mount)

bridge_ok() { [ -S "$RUN/sfpg/.s.PGSQL.5432" ] && pg_isready -q -h "$RUN/sfpg" -t 5; }

# 1. Pont vers la base StreamFusion (si elle est dans un pod local non publié)
if [ "${SF_DB_BRIDGE:-0}" = 1 ] && ! bridge_ok; then
  pkill -f "UNIX-LISTEN:$RUN/sfpg/.s.PGSQL.543[2]"; rm -f "$RUN/sf-db-bridge.pid"; sleep 1
  "$(dirname "$0")/sf-db-bridge.sh" || exit 1
  for _ in $(seq 1 10); do bridge_ok && break; sleep 3; done
  bridge_ok || { echo "pont : Postgres StreamFusion injoignable"; exit 1; }
  echo "pont relancé"
fi

# 2. sffs (nouvelle version bin/sffs.new appliquée seulement si sffs doit redémarrer)
SFFS_NEW=0
P=$(cat "$RUN/sffs.pid" 2>/dev/null)
if ! { alive "$P" sffs && mountpoint -q "$MNT"; }; then
  if alive "$P" sffs; then kill -TERM "$P"; for _ in $(seq 1 20); do alive "$P" sffs || break; sleep 1; done; kill -KILL "$P" 2>/dev/null; fi
  fusermount3 -uz "$MNT" 2>/dev/null; rm -f "$RUN/sffs.pid"
  [ -f "$STACK/bin/sffs.new" ] && mv "$STACK/bin/sffs.new" "$STACK/bin/sffs"
  (set -a; . "$CONF/sffs.env"; set +a; cd "$RUN" && setsid nohup "$STACK/bin/sffs" >>"$RUN/sffs.log" 2>&1 </dev/null & echo $! > "$RUN/sffs.pid")
  # Premier chargement : ~1 min pour les films, quelques minutes de plus avec les séries
  # (bien plus long au tout premier démarrage : remplissage du cache TMDB).
  for _ in $(seq 1 1800); do mountpoint -q "$MNT" && break; alive "$(cat "$RUN/sffs.pid")" sffs || break; sleep 1; done
  mountpoint -q "$MNT" || { echo "sffs n'a pas démarré :"; tail -5 "$RUN/sffs.log"; exit 1; }
  echo "sffs monté (pid $(cat "$RUN/sffs.pid"))"
  SFFS_NEW=1
fi

# 3. Pod : postgres + redis
podman start "$PG" "$REDIS" >/dev/null || exit 1
add_dummy_ip

# 4. Silo : (re)démarré si sffs vient de repartir, s'il est arrêté ou s'il ne répond plus.
if ! podman container exists "$APP"; then run_app; echo "silo créé"
elif [ "$(podman inspect "$APP" --format '{{.State.Running}}')" != true ]; then podman start "$APP" >/dev/null; echo "silo démarré"
elif [ $SFFS_NEW = 1 ] || ! health; then podman stop -t 120 "$APP" >/dev/null; podman start "$APP" >/dev/null; echo "silo redémarré"
fi
wait_health && { echo "OK : http://127.0.0.1:$SILO_WEB_PORT"; exit 0; }
echo "silo ne répond pas : podman logs $APP"; exit 1
