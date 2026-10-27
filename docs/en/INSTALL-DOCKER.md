# Installing with Docker Compose (without SSDV2)

*Français : [../INSTALL-DOCKER.md](../INSTALL-DOCKER.md)*

## Prerequisites

- **Docker** 23 or newer (BuildKit), with Docker Compose v2.
- `/dev/fuse` present, and `/mnt` with shared propagation (`findmnt -o PROPAGATION /mnt` shows `shared`).
  This is the default with systemd. Otherwise: `sudo mount --make-rshared /`.
- StreamFusion reachable: ideally on a shared Docker network, otherwise by published address and port.

## Step by step

### 1. Get the repository and build the images

```bash
git clone https://github.com/Haldir31/sffs.git ~/sffs && cd ~/sffs
./build-images.sh          # sffs (1 to 2 min) then Silo (5 to 15 min)
```

Instead of building, you can pull the published images (`ghcr.io/haldir31/sffs:latest` and
`ghcr.io/haldir31/silo-sffs:latest`, amd64 and arm64) and tag them `localhost/sffs:latest` and
`localhost/silo-sffs:latest`.

### 2. Read-only role in the StreamFusion database

With an administrator account of the StreamFusion database:

```sql
CREATE ROLE sffs_reader LOGIN PASSWORD 'a-password';
ALTER ROLE sffs_reader SET default_transaction_read_only = on;
GRANT CONNECT ON DATABASE streamfusion TO sffs_reader;
GRANT USAGE ON SCHEMA public TO sffs_reader;
GRANT SELECT ON public.torrent_items TO sffs_reader;
```

### 3. Configuration

```bash
cp docker/.env.example docker/.env && chmod 600 docker/.env
deploy/bin/sf-config.sh 'https://your-streamfusion/xxxx/manifest.json'   # -> SF_STREAM_CFG, SF_PLAYBACK_CFG
# or, without an addon URL: automatic generation (StreamFusion API key + AllDebrid key)
SF_URL=http://<streamfusion-ip>:8080 SF_PUBLIC=https://your-streamfusion SF_API_KEY=<key> AD_TOKEN=<key> \
  deploy/bin/sf-make-config.sh
nano docker/.env
```

Fill in:
- **Silo:** `SILO_SECRET_KEY` and `SILO_DB_PASS` (`openssl rand -hex 32`).
- **StreamFusion:** `SF_STREAM_CFG`, `SF_PLAYBACK_CFG`, then `SF_PG_DSN` and `SF_BASE` for your StreamFusion setup.
- **Network:** `SF_NETWORK`, StreamFusion's Docker network. Find it with `docker network ls`.
- **TV shows:** `SFFS_TMDB_KEY`.
- **Optional:** `SFFS_CACHED_ONLY=1` to only offer versions already cached at AllDebrid (far fewer titles,
  see [INSTALL-SSD.md](INSTALL-SSD.md#cached-only-mode-sffs_cached_only-1)); `SFFS_DEBRID_PROXY` if your
  server's address is refused by AllDebrid (same proxy as StreamFusion).

### 4. Start

```bash
docker compose -f docker/compose.yml up -d
docker logs -f sffs        # wait for "monté sur /mnt/sffs/sf"
ls /mnt/sffs/sf/films | head
```

### 5. First Silo setup

1. Open `http://127.0.0.1:27461`, or better your HTTPS reverse proxy, then create the admin account.
2. Add the **Films** (`/mnt/sf/films`) and **Series** (`/mnt/sf/series`) libraries.
3. Run `docker/silo-key.sh` to enable targeted scans of new content.

Put an HTTPS reverse proxy in front of the `SILO_WEB_PORT` (web) and `SILO_JF_PORT` (Jellyfin apps) ports.

## Updating

```bash
git pull && ./build-images.sh && docker compose -f docker/compose.yml up -d
```
