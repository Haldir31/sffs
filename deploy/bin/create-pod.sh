#!/bin/bash
# Première installation : crée le pod podman avec Postgres (pgvector) et Redis pour Silo.
# Ne recrée rien de ce qui existe déjà. Le conteneur Silo est lancé ensuite par start.sh.
. "$(dirname "$0")/common.sh"
for f in pg.env silo.env sffs.env; do
  [ -f "$CONF/$f" ] || { echo "manque $CONF/$f (voir deploy/config/$f.example)" >&2; exit 1; }
done
chmod 600 "$CONF"/*.env
mkdir -p "$RUN" "$(sffs_mount)" "$STACK"/data/{plugins,artwork,compat,transcode,download-artifacts}

podman pod exists "$POD" || podman pod create --name "$POD" \
  -p 127.0.0.1:"$SILO_WEB_PORT":8080 -p 127.0.0.1:"$SILO_JF_PORT":8096 -p 127.0.0.1:"$SILO_ABS_PORT":13378 >/dev/null || exit 1

podman container exists "$PG" || podman run -d --pod "$POD" --name "$PG" --env-file "$CONF/pg.env" \
  --tmpfs /dev/shm:rw,size=256m,mode=1777 -v "$POD-pgdata":/var/lib/postgresql \
  docker.io/pgvector/pgvector:pg18 \
  postgres -c listen_addresses=localhost -c max_connections=50 -c shared_buffers=256MB \
           -c max_worker_processes=4 -c max_parallel_workers=2 >/dev/null || exit 1

podman container exists "$REDIS" || podman run -d --pod "$POD" --name "$REDIS" -v "$POD-redis":/data \
  docker.io/library/redis:alpine redis-server --io-threads 1 >/dev/null || exit 1

add_dummy_ip
echo "pod $POD prêt (postgres + redis). Étape suivante : deploy/bin/start.sh"
