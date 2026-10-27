# Installing with rootless Podman (without Docker)

*Français : [../INSTALL-PODMAN.md](../INSTALL-PODMAN.md)*

For a server without Docker or root access, such as a shared seedbox. sffs runs directly on the host, and
Silo, its database and Redis run in a podman pod. Scripts: `deploy/bin/`.

## Prerequisites

- **Linux** with FUSE 3 (`fusermount3`) usable by your user. `allow_other` is not needed: the mount is only
  read by your user's containers.
- **Podman** 4+ in rootless mode (this guide). For Docker: [INSTALL-SSD.md](INSTALL-SSD.md) or
  [INSTALL-DOCKER.md](INSTALL-DOCKER.md).
- **Go** 1.21 or newer. `go.mod` asks for Go 1.27, downloaded automatically by `go build` if needed.
- `git`, `curl`, `openssl`, `socat` and `pg_isready` (PostgreSQL client package).
  `socat` is only used for the bridge to a local, unpublished StreamFusion database (see step 3).
- A working **StreamFusion Reborn** instance, configured with **AllDebrid**, and access to its Postgres database.
- A **TMDB API key** (v3, free) for TV shows.
- About 10 GB free for the Silo image and the build. Silo's artwork then takes about 100 GB for 50,000
  movies and 15,000 shows.
- Memory: sffs uses about 3 GB with the whole catalogue, and Silo 3 to 6 GB.

## Step-by-step installation

All commands run as your user (not root). The installation folder is `~/sffs-stack`; to change it, export
`SFFS_STACK=/other/path` before each script.

### 1. Get the repository

```bash
git clone https://github.com/Haldir31/sffs.git ~/sffs
cd ~/sffs
mkdir -p ~/sffs-stack/config
```

### 2. General configuration

```bash
cp deploy/config/stack.env.example ~/sffs-stack/config/stack.env
nano ~/sffs-stack/config/stack.env
```

- Choose the **ports** (published on 127.0.0.1 only).
- `ADD_DUMMY_PRIVATE_IP=1` only if the server has **no** private IPv4 address. To find out: `ip -4 addr` only
  shows public or 100.64.x.x addresses. This is typical of a shared seedbox.
- `SF_DB_BRIDGE`: see step 3.

### 3. Access to the StreamFusion database

sffs reads StreamFusion's `torrent_items` table, **read-only**. Two cases:

- **The database is reachable** (published port, another server, private network): set `SF_DB_BRIDGE=0` and a
  regular URL in `SF_PG_DSN` at step 4. Ideally, use a read-only Postgres role:
  ```sql
  CREATE ROLE sffs LOGIN PASSWORD '…';
  GRANT CONNECT ON DATABASE streamfusion TO sffs;
  GRANT USAGE ON SCHEMA public TO sffs;
  GRANT SELECT ON torrent_items TO sffs;
  ```
- **StreamFusion runs in a podman pod on this server, without a published Postgres port**: set
  `SF_DB_BRIDGE=1` and `SF_POD=<pod name>`. `start.sh` will open a bridge (Unix socket) to the pod's Postgres,
  without changing anything on the StreamFusion side. The DSN then uses `host=<stack>/run/sfpg`.

### 4. sffs configuration

```bash
cp deploy/config/sffs.env.example ~/sffs-stack/config/sffs.env
chmod 600 ~/sffs-stack/config/sffs.env
```

Get the two StreamFusion configurations from your addon's install URL (the one ending with `/manifest.json`):

```bash
deploy/bin/sf-config.sh 'https://your-streamfusion/xxxxxxxx/manifest.json'
# SF_STREAM_CFG=gAAAA…
# SF_PLAYBACK_CFG=eyJ…      <- contains your AllDebrid token: secret
```

Then fill in `sffs.env`:
- **StreamFusion configurations:** `SF_STREAM_CFG` and `SF_PLAYBACK_CFG` (output above).
- **StreamFusion access:** `SF_PG_DSN` (step 3) and `SF_BASE`, StreamFusion's address as seen from this
  server, preferably local.
- **TV shows:** `SFFS_TMDB_KEY`.
- **Paths:** replace `/home/USER/` with your home folder.
- **Silo key:** leave `SFFS_SILO_KEY` empty for now (step 9).
- **Optional:** `SFFS_CACHED_ONLY=1` to only offer versions already cached at the debrid service.

Each setting is described in [ARCHITECTURE.md](ARCHITECTURE.md#sffs-settings).

### 5. Silo and database configuration

```bash
cp deploy/config/pg.env.example   ~/sffs-stack/config/pg.env
cp deploy/config/silo.env.example ~/sffs-stack/config/silo.env
chmod 600 ~/sffs-stack/config/*.env
openssl rand -hex 24   # -> POSTGRES_PASSWORD (pg.env) AND the password in DATABASE_URL (silo.env)
openssl rand -hex 32   # -> SECRET_KEY (silo.env)
```

### 6. Build sffs and dry-run it

```bash
deploy/bin/build-sffs.sh
# Dry run without mounting, limited to 2 movies, listing the selected versions:
( set -a; . ~/sffs-stack/config/sffs.env; set +a
  SFFS_TMDB=98,603 SFFS_SERIES=0 SFFS_DRY_RUN=1 SFFS_DRY_LIST=1 ~/sffs-stack/bin/sffs )
```

You should see lines like `Gladiator (2000) {tmdb-98} | 2160 | remux=true | …`. Otherwise, check `SF_PG_DSN`
and `SFFS_LANGS`.

### 7. Build the patched Silo image

```bash
deploy/bin/build-silo.sh        # 5 to 15 minutes
```

The script clones official Silo into `~/sffs-stack/src/silo-server`, checks out the tested commit
(`silo/BASE_COMMIT`), applies the patches and builds the `localhost/silo-sffs:latest` image.

### 8. Create the pod and start

```bash
deploy/bin/create-pod.sh        # pod + postgres + redis (once)
deploy/bin/start.sh             # bridge (optional) -> sffs -> silo
```

The first sffs start takes several minutes, because it fills the TMDB cache for TV shows. Later starts take
about 2 minutes. Then check:

```bash
ls ~/sffs-stack/mnt/films | head
curl -s http://127.0.0.1:27461/api/v1/health   # 200
```

### 9. First Silo setup

1. Open `http://127.0.0.1:<SILO_WEB_PORT>`, ideally through your HTTPS reverse proxy, and **create the admin
   account**.
2. Add two libraries:
   - **Films**, type movies, folder `/mnt/sf/films`
   - **Series**, type shows, folder `/mnt/sf/series`

   The first scan takes several hours with the whole catalogue, about 3 h for 200,000 movie versions. Files
   are not read during the scan: their characteristics come from sffs.
3. Create the API key that lets sffs request targeted scans, then apply it:
   ```bash
   deploy/bin/make-silo-key.sh            # prints SFFS_SILO_KEY=sa_…
   nano ~/sffs-stack/config/sffs.env      # paste the key into SFFS_SILO_KEY
   deploy/bin/redeploy.sh                 # restarts sffs and Silo (2 to 5 min)
   ```

### 10. Automatic restart

```bash
crontab -e
```
```cron
@reboot     sleep 90 && $HOME/sffs/deploy/bin/start.sh    >> $HOME/sffs-stack/run/watchdog.log 2>&1
*/5 * * * * $HOME/sffs/deploy/bin/watchdog.sh >/dev/null 2>&1
```

To stop the stack by hand without the watchdog restarting it: `touch ~/sffs-stack/STOPPED`.

### 11. Client access

Put an HTTPS reverse proxy (Caddy, nginx…) in front of:
- `SILO_WEB_PORT` for the browser and the API;
- `SILO_JF_PORT` for Jellyfin-compatible apps: Infuse, Swiftfin, the Jellyfin app on Apple TV, etc.

## Updating

| What | Commands |
|---|---|
| sffs (after `git pull`) | `deploy/bin/build-sffs.sh && deploy/bin/redeploy.sh` |
| Silo, same base | `deploy/bin/build-silo.sh && deploy/bin/redeploy.sh` |
| Silo, new upstream version | `deploy/bin/build-silo.sh origin/main`: if the patches apply, test, then update `silo/BASE_COMMIT` |
| sffs settings | edit `sffs.env`, then `deploy/bin/redeploy.sh` |

New StreamFusion content needs no action: sffs detects it every minute and asks Silo to scan the affected
folders.

See also [OPERATIONS.md](OPERATIONS.md) for logs, checks and troubleshooting.
