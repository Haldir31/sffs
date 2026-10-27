# How it works

*Français : [../ARCHITECTURE.md](../ARCHITECTURE.md)*

## Overview

1. **Loading.** At startup, sffs reads the movie and TV sources from StreamFusion's `torrent_items` table
   (read-only). For TV shows, the episode list comes from TMDB, with a disk cache.
2. **Selection.** For each title, sffs keeps the best versions (see below). Each one becomes a virtual file:
   ```
   films/<Title> (<Year>) {tmdb-<id>}/<release> [<hash8>].mkv
   series/<Title> (<Year>) {tmdb-<id>}/Season 01/<Title> - S01E01 - <release> [<hash8>].mkv
   ```
   The file has the right size. Its characteristics (codecs, resolution, HDR, audio tracks) are derived from
   the release name and exposed in the `user.sf.probe` extended attribute.
3. **Scan.** The patched Silo recognises these files and reads `user.sf.probe` instead of analysing the
   video. A full scan therefore downloads nothing.
4. **Playback.** When a client starts playing, Silo "arms" the file (`getxattr user.sf.arm`). sffs then asks
   StreamFusion for the link (`/playback/…`, resolved server side: the client never sees the AllDebrid token)
   and streams the bytes on the fly, with HTTP Range requests.
5. **Protection.** A file that is not armed, or opened by a program not listed in `SFFS_ALLOW_COMM`, is
   refused without contacting the debrid service. An `ls`, a thumbnailer or an antivirus never triggers a download.
6. **Substitution.** If the chosen version is not cached at AllDebrid, sffs replaces it with a cached version
   of similar quality: a remux with another remux of the same quality first, otherwise the same quality, then
   the closest one. The `user.sf.served` attribute then gives the hash of the version actually served, and
   Silo analyses that replacement before deciding how to play it. If the original becomes cached again, sffs
   goes back to it.
7. **Updates.** Every minute (`SFFS_WATCH`), sffs looks for sources added or changed in StreamFusion, reloads
   only the affected titles and updates the tree **without unmounting**. It finds rows by their Postgres
   transaction number (`xmin`), not by `updated_at`: torrents received through StreamFusion's sync keep the
   date of their source and would never be seen. StreamFusion's background jobs (TMDB/IMDb matching) rewrite
   tens of thousands of rows without changing their sources, so sffs keeps a fingerprint of each title's
   sources (count and XOR of the `info_hash` hashes, computed by Postgres) and only reloads titles whose
   fingerprint changed. It then asks Silo to scan only the changed folders (`POST /api/v2/scan` with `path`).
   A full reload runs every `SFFS_RELOAD` (24 h) as a safety net.
8. **Silo libraries** (`SFFS_SILO_LIBRARIES=1`, SSDV2 app): as soon as the Silo API key exists, sffs creates
   the **Films** and **Séries** libraries (French metadata, `SFFS_SILO_LIBRARIES_LANG`) if no library already
   contains its folders. Only once: a library that was deleted is not recreated.

## Version selection

1. **Discarded:** duplicates (same torrent offered by several indexers), CAM, TS, TC, screener, R5, 3D, and
   disc images (ISO, BDMV, COMPLETE/Full BluRay, BD25/50/66/100, M2TS), which Silo cannot play. For a movie,
   a version whose year is more than one year away from the movie's year is also discarded. With
   `SFFS_CACHED_ONLY=1`, versions not seen in cache are discarded too.
2. **Language first.** Only the best language of `SFFS_LANGS` available for the title is kept. It is detected
   in the name (FRENCH, TRUEFRENCH, VFF, VFQ, VFI, VF2, MULTI…) or in the languages found by StreamFusion.
   With `SFFS_REQUIRE_PREFERRED_HD=1`, a title without any version in a preferred language is not shown.
3. **Ranking:** versions already known to be cached come first (StreamFusion flag or `known-cached.json`),
   then those with the most seeders.
4. **Quotas per quality:** 2160p, 1080p and 720p; SD and unknown quality are excluded when HD exists.
   Movies: `SFFS_PER_QUALITY` (5). Episodes: `SFFS_SERIES_PER_QUALITY` (2).
   **A 4K remux and a 1080p remux are always kept when one exists** in the right language.

## sffs settings

| Variable | Purpose | Default |
|---|---|---|
| `SF_PG_DSN` | StreamFusion Postgres database (read-only) | required |
| `SF_BASE` | StreamFusion URL as seen from the server | required |
| `SF_STREAM_CFG` | Encrypted Stremio config (source list, cache status) | required |
| `SF_PLAYBACK_CFG` | First segment of the `/playback/` URLs (contains the AllDebrid token) | required |
| `SFFS_MOUNT` | Mount point | required |
| `SFFS_ALLOW_COMM` | Programs allowed to read (regex on the process name). Recommended: `^(silo\|ffmpeg\|ffprobe)$` | `.` (all) |
| `SFFS_ALLOW_OTHER` | `1`: mount readable by other users (Silo under another UID, Docker) | `0` |
| `SFFS_ARM_TTL` | How long an arming stays valid | `6h` |
| `SFFS_LANGS` | Preferred languages, in order | `fr` |
| `SFFS_REQUIRE_PREFERRED_HD` | Skip titles without a version in a preferred language | `0` |
| `SFFS_PER_QUALITY` | Versions per quality for a movie (`0` = all) | `5` |
| `SFFS_TMDB` | Only the listed movies (TMDB IDs, tests) | all |
| `SFFS_SERIES` | Also expose TV shows | `0` |
| `SFFS_SERIES_PER_QUALITY` | Versions per quality and per episode | `2` |
| `SFFS_CACHED_ONLY` | `1`: only versions seen in cache at the debrid service (StreamFusion `availability` column, or torrents already seen cached by sffs); a title without a cached version is not shown. The cache status is part of the watch fingerprint: a version that becomes cached makes its title appear | `0` |
| `SFFS_SERIES_TMDB` | Only the listed shows | all |
| `SFFS_TMDB_KEY` | TMDB API key (required with TV shows) | |
| `SFFS_TMDB_CACHE` | Disk cache of TMDB answers | |
| `SFFS_KNOWN_CACHE` | Sources already seen cached | `$HOME/silo-test/run/known-cached.json` |
| `SFFS_DEBRID_PROXY` | Proxy for reading from the debrid service, e.g. `socks5://warp:1080` (the same as StreamFusion's): AllDebrid refuses datacenter addresses | none |
| `SFFS_WATCH` | Interval for detecting new content (`0` = off) | `1m` |
| `SFFS_RELOAD` | Full reload (`0` = never) | `24h` |
| `SFFS_SILO_URL`, `SFFS_SILO_KEY` | Silo API for targeted scans (empty = off) | |
| `SFFS_SILO_KEY_FILE` | File holding the Silo key, re-read on each pass (when `SFFS_SILO_KEY` is empty): the key can be created after startup | |
| `SFFS_SILO_LIBRARIES` | `1`: create the Films and Séries libraries in Silo, once (marker: `SFFS_SILO_LIBRARIES_DONE`) | |
| `SFFS_SILO_LIBRARIES_LANG`, `SFFS_SILO_FILMS_NAME`, `SFFS_SILO_SERIES_NAME` | Metadata language and names of the created libraries | `fr`, `Films`, `Séries` |
| `SFFS_SILO_FILMS`, `SFFS_SILO_SERIES` | Folder paths as seen by Silo | `/mnt/sf/films`, `/mnt/sf/series` (SSDV2: `/mnt/sffs/films`, `/mnt/sffs/series`) |
| `SFFS_DRY_RUN`, `SFFS_DRY_LIST` | Load without mounting, and list the selected versions | |

The playback service is fixed to AllDebrid (`"service":"AD"` in `playbackURL`, `sffs/main.go`).

## Extended attributes

| Attribute | Contents |
|---|---|
| `user.sf.id` | Internal version ID |
| `user.sf.probe` | JSON characteristics derived from the release name |
| `user.sf.arm` | Read by Silo to allow playback (arming) |
| `user.sf.armed` | Is the file armed? |
| `user.sf.cached` | Is the version (or a replacement) cached at AllDebrid? |
| `user.sf.served` | Hash of the source actually served (replacement included) |

## Silo changes (`silo/patches`)

| Patch | Purpose |
|---|---|
| 0001 | Recognise sffs files (`internal/sfvirtual`): characteristics read from `user.sf.probe`, arming before playback |
| 0002 | No probe repair and no "copy-safety" scan on sffs files (they would be downloaded) |
| 0003 | Probe repair only for an armed file, i.e. when it is being played |
| 0004 | Jellyfin compatibility: the version requested in PlaybackInfo is armed (Apple TV, Infuse…) |
| 0005 | An uncached version falls back to another version of the same title |
| 0006 | Analyse the source actually served (`user.sf.served`) when sffs substituted a version; after a failure, no new attempt for one hour |

The patches change nothing outside `SILO_SF_ROOT`: other libraries behave as in official Silo.

Silo finds the sffs files under `SILO_SF_ROOT`: `/mnt/sffs` with SSDV2 (same paths as the host), `/mnt/sf`
with Compose and Podman.
