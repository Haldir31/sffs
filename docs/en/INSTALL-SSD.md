# Installing with SSDV2

*Français : [../INSTALL-SSD.md](../INSTALL-SSD.md)*

Two apps of the SSDV2 catalogue:

| App | Purpose |
|---|---|
| `silo` | Official Silo, unmodified. Same folders as Plex, read-only: `~/Medias`, `~/local`, `/data`, `/mnt` (rslave) |
| `sffs` | The virtual file system (`sffs` container, mounted on `/mnt/sffs`), plus sffs support in `silo` |

The **`sffs`** app covers two cases:
- **Silo is already installed**: it is patched in place. Its database, accounts, libraries and subdomain are
  kept; only the image changes (official Silo + patches from `silo/patches`).
- **Silo is not installed**: sffs asks for Silo's subdomain, installs itself, then installs `silo`.

In both cases, sffs starts before Silo, and `<SETTINGS_STORAGE>/vars/silo.yml` is written: the `silo`
definition with the patched image and `SILO_SF_ROOT=/mnt/sffs`. SSDV2 uses it instead of the catalogue one, so
restarting `silo` keeps sffs support.

Outside `/mnt/sffs`, the patched Silo behaves like official Silo: a single Silo serves the usual libraries
(`~/Medias/Films`…) and the sffs ones (`/mnt/sffs/films`, `/mnt/sffs/series`).

Until they are merged into SSDV2, these apps live in the `feat/silo` branch of the
[Haldir31/ssdv2](https://github.com/Haldir31/ssdv2) fork.

## What installing sffs does

1. **Checks** that StreamFusion is installed by SSDV2 (`ssddb` registry) and running, that `/dev/fuse` exists
   and that `/mnt` has shared propagation. Nothing is created if a check fails.
2. **Clones this repository** into `<SETTINGS_STORAGE>/docker/<user>/sffs/src` (settings `sffs_repo`,
   `sffs_repo_version`).
3. **Images** `localhost/sffs` and `localhost/silo-sffs`: each image carries the git fingerprint of its
   sources (`sffs.tree` label). An up-to-date image is kept; otherwise it is **downloaded** from
   `ghcr.io/haldir31` (built by GitHub Actions, amd64 and arm64) if it matches the sources, and **built** on
   the server otherwise (5 to 15 min for Silo; force it with `sffs_build_local: true`).
4. **Version check**: if Silo's database was already migrated by a newer version than the patched image, the
   installation stops without changing anything.
5. **Configures sffs**, without going through StreamFusion's `/configure` page:
   - a **dedicated StreamFusion API key** (`sffs`, `api_keys` table);
   - the **AllDebrid key** from decypharr if it is installed, asked otherwise;
   - the **TMDB key** from StreamFusion, asked if missing;
   - the configuration encrypted by StreamFusion (`POST /api/config/encode`), as the web page does;
   - **StreamFusion's proxy** (`PROXY_URL`, usually `socks5://warp:1080`), without which AllDebrid refuses
     downloads from a server;
   - a **read-only Postgres role** in `stremio-postgres` (`sffs_reader`, `SELECT` on `torrent_items`).
6. **Writes `vars/silo.yml`** (patched image). A personal `vars/silo.yml` is kept as `silo.yml.avant-sffs`
   and used as the base.
7. **Starts sffs**, then **installs or redeploys `silo`** through SSDV2 (its pretask: database, Redis,
   Meilisearch, GPU; its posttask: DNS, admin account).
8. Creates **sffs's Silo API key** as soon as an admin account exists. sffs re-reads it without restarting,
   creates the libraries, and asks Silo for a targeted scan of each folder changed in StreamFusion.

Secrets: `<SETTINGS_STORAGE>/docker/<user>/sffs-secrets/` (mode 0600), generated or asked once.

## Prerequisites

- SSDV2, with **StreamFusion installed by SSDV2** and **working**: the Stremio addon must already show cached
  sources. On a server (datacenter), AllDebrid only accepts StreamFusion through SSDV2's `warp` proxy; check
  that it is connected (see Troubleshooting).
- An **AllDebrid** account and a **TMDB** key (already set in StreamFusion).
- `/dev/fuse` present, and `/mnt` with shared propagation (`findmnt -o PROPAGATION /mnt` shows `shared`).
  This is the default, and decypharr or zurg already need it.

## Installation

1. Install the **sffs** app from SSDV2. If Silo is not installed, it asks for its subdomain (Silo's Traefik
   authentication is set to `aucune`/none: Silo manages its own accounts, and Jellyfin apps cannot pass a
   Traefik authentication).
2. **Admin account**: a new Silo gets it automatically, with the SSDV2 user name and password (like the other
   catalogue apps). If the SSDV2 password is shorter than 8 characters (Silo's minimum), create the account
   in the web interface, then restart the sffs app.
3. **Libraries**: sffs creates **Films** (`/mnt/sffs/films`) and **Séries** (`/mnt/sffs/series`) itself,
   with French metadata, as soon as its Silo API key exists and `/mnt/sffs` is mounted (1 to 20 minutes after
   installation, depending on the TMDB cache). Only once: a library that was deleted is not recreated.

   The first scan takes several hours for the whole catalogue. Nothing is downloaded during the scan.
   If sffs restarts during that scan, Silo sees the folders disappear for a minute and marks the library
   unreachable (without deleting anything): sffs then restarts its scan by itself within 5 minutes.
4. New StreamFusion content then shows up in Silo within a few minutes.

Clients:
- **Browser:** `https://<subdomain>.<domain>`
- **Jellyfin apps** (Apple TV, Infuse, Swiftfin…): server `https://<subdomain>-jf.<domain>`

## sffs app options

To change an option: copy `includes/dockerapps/vars/sffs.yml` from the SSDV2 catalogue to
`<SETTINGS_STORAGE>/vars/sffs.yml`, edit it, then restart the sffs app. The tree is updated in place, and Silo
sees the changes on its next scan.

| Option | Default | Effect |
|---|---|---|
| `sffs_langs` | `fr` | Wanted languages, in order of preference (`fr,en`…) |
| `sffs_per_quality` | `5` | Versions kept per quality (2160p, 1080p, 720p…) for a movie |
| `sffs_series` | `1` | `0`: movies only |
| `sffs_series_per_quality` | `2` | Versions per quality and per episode |
| `sffs_remux` | `1` | `0`: no REMUX (smaller files, no disc-quality 4K) |
| `sffs_light` | `0` | `1`: also offer light encodes (HDLight, 4KLight, mHD…) |
| `sffs_cached_only` | `0` | `1`: **only versions already cached at AllDebrid**. See below |
| `sffs_repo`, `sffs_repo_version` | this repository, `main` | Sources of sffs and of the Silo patches |
| `sffs_registry` | `ghcr.io/haldir31` | Image registry; empty: build on the server |
| `sffs_build_local` | `false` | `true`: always build the images on the server |
| `sffs_alldebrid_key` | | AllDebrid key, if decypharr is not installed and you do not want to be asked |
| `sffs_debrid_proxy_override` | StreamFusion's proxy | Proxy for reading from AllDebrid; `''` for none |

Always applied, without an option: at least one 2160p REMUX and one 1080p REMUX when they exist (when
`sffs_remux` is `1`), and never CAM, screener, 3D or disc images (ISO, BDMV, COMPLETE/Full BluRay…), which Silo cannot play.

### "Cached only" mode (`sffs_cached_only: '1'`)

sffs only offers versions seen in cache at AllDebrid: those StreamFusion marked as available (`availability`
column), and those sffs itself saw cached during a playback. A title without any cached version is not shown.

- **Pro:** every version offered starts without waiting for AllDebrid to download it.
- **Con:** far fewer titles. Measured on a database of 1.2 million torrents: about 16,700 movies (36,400
  versions) instead of 43,250 (162,300 versions).
- **Over time:** when a version becomes cached, the watch sees it and the title appears within a minute.
  AllDebrid's cache may also have been emptied since StreamFusion checked: at playback, sffs then replaces the
  version with a cached source of the same quality, as in normal mode.
- **Turning it on or off** adds or removes thousands of titles: Silo marks them missing (or adds them) on its
  next scans, without touching the watch history of the titles that remain.

## Day to day

| Action | How |
|---|---|
| Update sffs and the patched Silo | restart the sffs app (sources updated, images downloaded or rebuilt if needed, Silo redeployed) |
| Restart sffs | `docker restart sffs` (Silo sees the new mount without restarting) |
| Logs | `docker logs -f sffs` · `docker logs -f silo` |
| Change an sffs option | see [sffs app options](#sffs-app-options) |
| Remove sffs | remove the sffs app: `vars/silo.yml` is removed, and silo goes back to the official image on its next restart (its database is kept). The `sffs_reader` role stays in the StreamFusion database (`DROP ROLE sffs_reader`) |

**Backup**: the `silo` app backs up its database every night (`pg_dump`, systemd timer `silo-backup.timer`)
into `<SETTINGS_STORAGE>/docker/<user>/silo/backups/`, 7 days kept. Also keep `silo-secrets/SILO_SECRET_KEY`:
without it, a restored database cannot be read. Restore:
`docker exec -i db-silo pg_restore -U silo -d silo --clean --if-exists < file.dump`.

**Silo updates**: every week, GitHub Actions carries the patches over to the latest official Silo, runs the
tests and opens a pull request on this repository; once merged, the images are rebuilt and the next sffs
restart deploys them.

Do not update `silo` to a newer official image while sffs is installed: the database would be migrated past
the patched image. Silo updates go through sffs.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `/mnt/sffs` empty or "Transport endpoint is not connected" in Silo while it is populated on the host | Propagation: `/mnt` must be `shared` on the host. sffs mounts `/mnt` as `rshared`, Silo mounts `/mnt` as `rslave` (never `/mnt/sffs` itself, which would freeze a dead mount after an sffs restart). |
| `Transport endpoint is not connected` on the host | sffs was stopped abruptly: `docker restart sffs` (the container cleans the dead mount at startup). |
| Installation refuses: "Silo est plus récent que l'image modifiée" (Silo is newer than the patched image) | The database comes from a Silo newer than `silo/BASE_COMMIT`. Wait for a patch update. |
| sffs refuses every read (`refus open … processus non autorisé` in the logs) | `pid: host` missing: sffs cannot see which program reads. Check `docker inspect -f '{{.HostConfig.PidMode}}' sffs`. |
| No cached version (`cache movie/… : 30 sources, 0 en cache` in `docker logs sffs`), StreamFusion logs `NO_SERVER` | AllDebrid refuses the server's address: the `warp` proxy is disconnected. Test: `docker run --rm --network traefik_proxy curlimages/curl -s -x socks5h://warp:1080 https://www.cloudflare.com/cdn-cgi/trace \| grep warp` must show `warp=on`. Otherwise `docker restart warp`. The container can stay "Up (unhealthy)" with a dead WARP daemon: Docker does not restart it by itself. |
| New StreamFusion content does not reach Silo | No API key: restart the sffs app after creating the admin account (`sffs-secrets/SFFS_SILO_KEY` must exist). |
| Library marked unreachable (`dead_root`), scan with errors | sffs restarted during a scan. Nothing was deleted, and sffs restarts the scan within 5 minutes (`silo : bibliothèque … scan relancé` in its logs). |

See also [OPERATIONS.md](OPERATIONS.md) and [ARCHITECTURE.md](ARCHITECTURE.md).
