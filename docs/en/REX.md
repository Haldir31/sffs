# sffs as a rex source (Plex apps)

[rex](https://github.com/debridmediamanager/rex) shows itself to the official Plex apps as a Plex
server that owns no files. sffs can feed it directly: same StreamFusion catalog, same version
selection, bytes read from AllDebrid only when someone plays. The FUSE mount and Silo keep working
alongside.

> **Experimental.** rex is an independent project, not made or endorsed by Plex. Its author
> advises claiming it with a free Plex account made for it, then sharing it with your own account.

## How it works

- **Catalog.** sffs registers a source in rex (`PUT /rex/sources/<name>`), sends the whole catalog
  at startup (`/rex/feed/<name>/snapshot`, 300 items per request), then only the folders that
  changed (StreamFusion watch and full reloads). Each file is placed by its TMDB id (movie, or show
  with season and episode); rex groups the versions of a title.
- **Bytes.** sffs listens on `SFFS_REX_LISTEN` (127.0.0.1 by default) and requires a bearer token
  that rex gets when the source registers. A request from rex is relayed to AllDebrid with the same
  `Range`, through `SFFS_DEBRID_PROXY` when set. A version no longer cached is replaced by a cached
  one, as for Silo.
- **Version order.** A Plex app plays a title's first version: the server does not choose, it
  converts the one asked for. sffs therefore sends each title's versions from the most compatible
  (1080p, then 720p, then 4K; H.264 before HEVC; SDR; AC3/E-AC3/AAC audio before DTS/TrueHD) to the
  least, in waves (rex orders versions by creation). `SFFS_REX_ORDER=quality` puts the best quality
  first (4K REMUX) for players that need no conversion. The order is set when rex creates the title.
- **No guard.** rex analyzes no file: only playback (or a conversion) reads bytes.
- **Technical info.** rex needs an ffprobe result per file. At first sffs builds one from what
  StreamFusion parses out of the release name (codecs, resolution, HDR, languages), with no duration
  or real tracks. On first playback sffs probes the source actually served with `ffprobe` (format,
  tracks, subtitles, chapters, duration), keeps the result in `SFFS_REX_STATE/probes/` and updates
  the file in rex on the same key (watch history kept). The size is sent only once known exactly.

## Setup

1. rex **nightly 2026.10.25 or newer** (feed API), claimed by a Plex account: before the claim rex
   fetches no titles or posters and does not group a movie's versions (it does once claimed).
2. `ffprobe` installed next to sffs (or `SFFS_FFPROBE`).
3. In sffs's configuration:

   ```sh
   SFFS_REX_URL=http://127.0.0.1:32400      # rex
   SFFS_REX_LISTEN=127.0.0.1:32545          # sffs byte server
   # SFFS_REX_BYTES_URL=http://sffs:32545   # if rex reaches it another way (container)
   # SFFS_REX_SOURCE=sffs                    # source name in rex
   # SFFS_REX_STATE=~/.cache/sffs-rex        # token and probes
   ```

4. A reverse proxy on rex's machine must **block `/rex/`**: rex accepts the feed API from any local
   address.

To limit the catalog during a trial: `SFFS_TMDB` (movies) and `SFFS_SERIES_TMDB` (shows). rex
identifies a few hundred files per minute on its first fill.

The variables are listed in the [French page](../REX.md#variables).

## Known limits

- Before its first playback a file has no duration and no real track list: a Plex app may pick a
  needless conversion or show a wrong progress bar.
- A replaced (uncached) version is probed as served; if the replacement changes later, the probe is
  redone on the next playback.
- A file removed from sffs goes to rex's trash, as in Plex.
