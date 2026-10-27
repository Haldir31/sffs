# Fonctions communes aux scripts de deploy/bin (à sourcer).
# SFFS_STACK : dossier d'installation (défaut $HOME/sffs-stack).
set -u
STACK=${SFFS_STACK:-$HOME/sffs-stack}
CONF=$STACK/config
RUN=$STACK/run
REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
export PATH=/usr/local/bin:/usr/bin:/bin:$HOME/bin:$HOME/go/bin:$PATH
# Podman rootless : un TMPDIR sur le disque de l'utilisateur évite des erreurs crun sur
# certains hôtes où /tmp est partagé ou nettoyé.
export TMPDIR=${TMPDIR:-$HOME/.tmp}
mkdir -p "$TMPDIR"

[ -f "$CONF/stack.env" ] || { echo "manque $CONF/stack.env (voir deploy/config/stack.env.example)" >&2; exit 1; }
set -a; . "$CONF/stack.env"; set +a

APP=$POD-app PG=$POD-postgres REDIS=$POD-redis

health() { [ "$(curl -s -m 10 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SILO_WEB_PORT/api/v1/health")" = 200 ]; }
alive() { [ -n "${1:-}" ] && [ "$(cat "/proc/$1/comm" 2>/dev/null)" = "$2" ]; }
sffs_mount() { (set -a; . "$CONF/sffs.env"; echo "$SFFS_MOUNT"); }

# Exécute une commande dans l'espace réseau du pod (même si podman a perdu son état).
pod_netns() {
  local infra ns c p
  infra=$(podman pod inspect "$POD" --format '{{.InfraContainerID}}') || return 1
  ns=$(podman inspect "$infra" --format '{{.NetworkSettings.SandboxKey}}')
  if [ -n "$ns" ]; then podman unshare nsenter --net="$ns" "$@"
  else c=$(pgrep -f "conmon.*-c $infra" | head -1); p=$(pgrep -P "$c" | head -1)
    [ -n "$p" ] || return 1; nsenter -t "$p" -U -n --preserve-credentials "$@"; fi
}

add_dummy_ip() {
  [ "${ADD_DUMMY_PRIVATE_IP:-0}" = 1 ] || return 0
  pod_netns ip -4 addr show dev lo | grep -q 10.10.10.10 || pod_netns ip addr add 10.10.10.10/32 dev lo
}

# Lance (ou recrée) le conteneur Silo, toujours APRÈS sffs : il voit le montage tel qu'il
# était à son démarrage.
run_app() {
  local mnt; mnt=$(sffs_mount)
  podman run -d --pod "$POD" --name "$APP" --env-file "$CONF/silo.env" \
    -e SILO_POC_REMOTE=1 -e SILO_SF_ROOT=/mnt/sf --stop-timeout=120 \
    -v "$mnt":/mnt/sf:ro \
    -v "$STACK/data/plugins":/var/lib/silo/plugins -v "$STACK/data/artwork":/var/lib/silo/artwork \
    -v "$STACK/data/compat":/var/lib/silo/compat -v "$STACK/data/transcode":/tmp/silo-transcode \
    -v "$STACK/data/download-artifacts":/tmp/silo-download-artifacts \
    "$SILO_IMAGE" >/dev/null
}

wait_health() {
  for _ in $(seq 1 120); do health && return 0; sleep 5; done
  return 1
}
