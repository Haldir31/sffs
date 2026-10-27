# Operations and troubleshooting

*Français : [../EXPLOITATION.md](../EXPLOITATION.md)*

The paths below are those of the Podman installation (`~/sffs-stack`). With Docker or SSDV2, logs are
available with `docker logs sffs` and `docker logs silo`; see also the troubleshooting part of
[INSTALL-SSD.md](INSTALL-SSD.md).

## Where to look

| What | Where |
|---|---|
| sffs log | `~/sffs-stack/run/sffs.log` |
| Silo log | `podman logs --since 10m <POD>-app` |
| Watchdog | `~/sffs-stack/run/watchdog.log` |
| StreamFusion bridge | `~/sffs-stack/run/sf-db-bridge.log` |

Useful lines in the sffs log (messages are in French):
- `N films, M versions chargés`: full load.
- `monté sur …`: mount ready.
- `arm <id> "<version>" (par silo)`: Silo started a playback, the version is allowed.
- `resolve <id> (<hash>) -> <server> en …ms`: download link obtained from AllDebrid.
- `refus open … non armé` / `processus non autorisé`: the guard blocked a read (scan, Plex…).
- `surveillance : N titres réécrits…, M avec des sources modifiées`: change detection in StreamFusion.
- `surveillance : … -> X fichiers ajoutés, Y retirés`: new StreamFusion content added to the tree.
- `silo : N scans ciblés demandés`: scan requests sent to Silo. Silo limits the rate to about 20 requests per
  minute; the rest goes on the next pass.
- `silo : bibliothèque "Films" créée`: library created in Silo.
- `substitution <id> "<version>" -> <hash> "<file>"`: uncached version replaced at playback.

In the Silo logs:
- `streamfusion: probe de la source servie`: analysis of a replacement or of the version being played.
- `streamfusion: probe de la source servie impossible`: ffprobe could not analyse the version (no new attempt
  for one hour).
- `playback plan decided … outcome=terminal reason=…`: playback refused, with the reason and `file_id`.

## Quick checks

```bash
mountpoint ~/sffs-stack/mnt                       # the mount is active
ls ~/sffs-stack/mnt/films | wc -l                 # number of movies exposed
f=$(ls -d ~/sffs-stack/mnt/films/*/ | head -1); getfattr -d "$f"* | head   # attributes of a file
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:27461/api/v1/health
```

## Stop, start

```bash
touch ~/sffs-stack/STOPPED          # disables the watchdog
podman stop <POD>-app               # Silo
kill $(cat ~/sffs-stack/run/sffs.pid)   # sffs unmounts cleanly
rm ~/sffs-stack/STOPPED && ~/sffs/deploy/bin/start.sh
```

**Important rule (Podman):** Silo must always restart **after** sffs. A container started before an sffs
restart keeps the old, dead mount and shows "Transport endpoint is not connected". `start.sh` and
`redeploy.sh` follow this order. (With SSDV2, Silo mounts the whole of `/mnt` and sees a new mount without
restarting.)

## Known issues

| Symptom | Cause and fix |
|---|---|
| `podman exec` fails with a crun error "container does not exist" | Shared `TMPDIR`: `export TMPDIR=$HOME/.tmp` (already done by the scripts). |
| Silo crashes at startup with an ID error (sonyflake) | The host has no private IPv4: `ADD_DUMMY_PRIVATE_IP=1` in `stack.env`, then `start.sh`. |
| `fusermount3: option allow_other only allowed if 'user_allow_other' is set` | sffs does not use `allow_other` in this setup; the error comes from another FUSE program. |
| Playback: "The playback transport failed before media became ready" | Substituted version described with the original's characteristics: check that patch 0006 (source actually served) is in the image and look for `substitution` in the sffs log. |
| Playback: "This file hasn't finished scanning" | ffprobe could not analyse the version (`probe de la source servie impossible` in the Silo log). Disc images are already discarded; report the release name if it is a new kind of unreadable release. |
| Nothing shows up in `films/` | Language: with `SFFS_REQUIRE_PREFERRED_HD=1`, only titles with a version in `SFFS_LANGS` are shown. Test with `SFFS_DRY_RUN=1 SFFS_DRY_LIST=1`. |
| New content does not reach Silo | `SFFS_SILO_KEY` empty or invalid: look for `silo :` in the sffs log. Silo's nightly scan (2 am) catches up anyway. |
| Host with many CPUs and a process limit | Keep `GOMAXPROCS=4` (sffs and Silo): otherwise Go creates one thread per CPU. |

## Backup

To back up:
- `~/sffs-stack/config/`: secrets;
- the `<POD>-pgdata` volume: Silo database, with users, progress and favourites. Command:
  `podman exec <POD>-postgres pg_dump -U silo silo | gzip > silo.sql.gz`

Artwork (`data/artwork`) is downloaded again. sffs has no state of its own, apart from the
`tmdb-tv.json` and `known-cached.json` caches.
