# sffs : StreamFusion comme bibliothèque Silo

*English: [README.en.md](README.en.md)*

`sffs` est un système de fichiers virtuel (FUSE, lecture seule) qui présente le catalogue d'une instance
**StreamFusion Reborn** comme une bibliothèque de films et de séries pour **Silo**, sans rien stocker sur
disque. La vidéo n'est lue chez **AllDebrid** qu'au moment où quelqu'un lance une lecture.

```
StreamFusion ──base Postgres (lecture seule)──▶ sffs ──FUSE──▶ /mnt/sffs/{films,series}
     ▲                                            │                      │ (lecture seule)
     └──── /playback (lien AllDebrid) ◀───────────┘ à la lecture         ▼
                                                     Silo modifié (+ PostgreSQL pgvector + Redis)
                                                                         ▼
                                              navigateur, applications Jellyfin (Apple TV, Infuse…)
```

## Choisir son installation

| Situation | Guide |
|---|---|
| Serveur installé avec **SSDV2** ([projetssd/ssdv2](https://github.com/projetssd/ssdv2)) et StreamFusion installé par SSDV2 | **[docs/INSTALL-SSD.md](docs/INSTALL-SSD.md)** : application `sffs` du catalogue SSDV2 (patche le Silo existant, ou l'installe) |
| Serveur **Docker** sans SSDV2 | [docs/INSTALL-DOCKER.md](docs/INSTALL-DOCKER.md) : `docker/compose.yml` |
| Pas de Docker ni de root (**Podman rootless**, seedbox mutualisée) | [docs/INSTALL-PODMAN.md](docs/INSTALL-PODMAN.md) : scripts `deploy/bin/` |

Dans les trois cas, il faut :
- une instance **StreamFusion Reborn** configurée avec **AllDebrid**, avec accès à sa base Postgres ;
- l'URL d'installation de l'addon (`…/manifest.json`) ;
- une **clé API TMDB** (v3, gratuite) pour les séries.

Côté ressources :
- environ 10 Go pour construire les images ;
- environ 100 Go d'affiches Silo pour 50 000 films et 15 000 séries ;
- environ 3 Go de mémoire pour sffs, et 3 à 6 Go pour Silo.

## Contenu du dépôt

| Chemin | Contenu |
|---|---|
| `sffs/` | Code Go de sffs et son `Dockerfile` |
| `silo/` | 6 patchs à appliquer sur Silo officiel au commit `silo/BASE_COMMIT`, et le `Dockerfile` de l'image modifiée |
| `build-images.sh` | Construit les images `localhost/sffs` et `localhost/silo-sffs` (Docker ou Podman) |
| `docker/` | `compose.yml` pour Docker sans SSDV2 |
| `deploy/` | Scripts et modèles de configuration pour Podman rootless |
| `.github/workflows/` | Construction et publication des images sur `ghcr.io/haldir31` (amd64, arm64) ; vérification hebdomadaire que les patchs s'appliquent sur Silo `main` |
| `docs/` | Documentation en français ; `docs/en/` : en anglais. Guides d'installation, [fonctionnement](docs/ARCHITECTURE.md), [exploitation et dépannage](docs/EXPLOITATION.md) |

## Licence

Silo est publié sous licence AGPL-3.0. Les patchs de `silo/` sont des modifications de Silo et suivent
cette licence. Pour distribuer l'image Silo modifiée, il faut aussi publier ces sources.
