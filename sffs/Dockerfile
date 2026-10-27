# Image de sffs. Le conteneur a besoin de /dev/fuse, de CAP_SYS_ADMIN, d'apparmor non confiné,
# d'un montage du point de montage en propagation "rshared" et de pid=host (pour reconnaître
# le programme qui lit un fichier, voir SFFS_ALLOW_COMM). Voir docker/compose.yml.
FROM golang:1.27 AS build
ENV GOMAXPROCS=4 GOFLAGS=-p=4 CGO_ENABLED=0 GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go build -trimpath -ldflags="-s -w" -o /sffs .

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends fuse3 ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /sffs /usr/local/bin/sffs
# Caches persistants (TMDB, sources déjà vues en cache).
ENV SFFS_TMDB_CACHE=/config/tmdb-tv.json SFFS_KNOWN_CACHE=/config/known-cached.json \
    SFFS_MOUNT=/mnt/sffs GOMAXPROCS=4
VOLUME /config
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
# sffs démonte proprement à l'arrêt (SIGTERM) ; l'entrée nettoie un montage resté bloqué
# après un arrêt brutal (« Transport endpoint is not connected »).
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
