# sffs: StreamFusion as a Silo library

*Français : [README.md](README.md)*

`sffs` is a virtual, read-only file system (FUSE) that presents the catalogue of a **StreamFusion Reborn**
instance as a movie and TV library for **Silo**, without storing anything on disk. The video is only read from
**AllDebrid** when someone starts playing it.

```
StreamFusion ──Postgres database (read-only)──▶ sffs ──FUSE──▶ /mnt/sffs/{films,series}
     ▲                                            │                      │ (read-only)
     └──── /playback (AllDebrid link) ◀───────────┘ on playback          ▼
                                                     Patched Silo (+ PostgreSQL pgvector + Redis)
                                                                         ▼
                                              browser, Jellyfin apps (Apple TV, Infuse…)
```

## Choosing an installation

| Situation | Guide |
|---|---|
| Server installed with **SSDV2** ([projetssd/ssdv2](https://github.com/projetssd/ssdv2)), StreamFusion installed by SSDV2 | **[docs/en/INSTALL-SSD.md](docs/en/INSTALL-SSD.md)**: `sffs` app of the SSDV2 catalogue (patches the existing Silo, or installs it) |
| **Docker** server without SSDV2 | [docs/en/INSTALL-DOCKER.md](docs/en/INSTALL-DOCKER.md): `docker/compose.yml` |
| No Docker and no root (**rootless Podman**, shared seedbox) | [docs/en/INSTALL-PODMAN.md](docs/en/INSTALL-PODMAN.md): `deploy/bin/` scripts |

In all three cases you need:
- a **StreamFusion Reborn** instance configured with **AllDebrid**, with access to its Postgres database;
- the addon install URL (`…/manifest.json`);
- a **TMDB API key** (v3, free) for TV shows.

Resources:
- about 10 GB to build the images;
- about 100 GB of Silo artwork for 50,000 movies and 15,000 shows;
- about 3 GB of memory for sffs, and 3 to 6 GB for Silo.

## Repository contents

| Path | Contents |
|---|---|
| `sffs/` | sffs Go code and its `Dockerfile` |
| `silo/` | 7 patches applied to official Silo at commit `silo/BASE_COMMIT`, and the `Dockerfile` of the patched image |
| `build-images.sh` | Builds the `localhost/sffs` and `localhost/silo-sffs` images (Docker or Podman) |
| `.github/workflows/` | Builds and publishes the images to `ghcr.io/haldir31` (amd64, arm64); weekly check that the patches still apply to Silo `main` |
| `docker/` | `compose.yml` for Docker without SSDV2 |
| `deploy/` | Scripts and configuration templates for rootless Podman |
| `docs/` | Documentation in French; `docs/en/`: in English. Installation guides, [how it works](docs/en/ARCHITECTURE.md), [operations and troubleshooting](docs/en/OPERATIONS.md) |

## License

Silo is released under the AGPL-3.0 license. The patches in `silo/` are modifications of Silo and follow that
license. Distributing the patched Silo image requires publishing these sources too.
