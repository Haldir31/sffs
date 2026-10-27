#!/bin/bash
# Applique une nouvelle version : arrête Silo, redémarre sffs (bin/sffs.new s'il existe),
# puis recrée le conteneur Silo sur $SILO_IMAGE. Coupure de 2 à 5 minutes.
. "$(dirname "$0")/common.sh"
touch "$STACK/STOPPED"   # le watchdog n'intervient pas pendant l'opération
trap 'rm -f "$STACK/STOPPED"' EXIT
MNT=$(sffs_mount)
if podman container exists "$APP"; then podman stop -t 120 "$APP" >/dev/null; podman rm "$APP" >/dev/null; fi
P=$(cat "$RUN/sffs.pid" 2>/dev/null)
if alive "$P" sffs; then
  kill -TERM "$P"; for _ in $(seq 1 20); do alive "$P" sffs || break; sleep 1; done
  alive "$P" sffs && kill -KILL "$P" && sleep 1
fi
fusermount3 -uz "$MNT" 2>/dev/null; rm -f "$RUN/sffs.pid"
"$(dirname "$0")/start.sh"
