# Fonctionnement

## Vue d'ensemble

1. **Chargement.** Au démarrage, sffs lit les sources de films et de séries dans la table `torrent_items`
   de StreamFusion (lecture seule). Pour les séries, la liste des épisodes vient de TMDB, avec un cache disque.
2. **Sélection.** Pour chaque titre, sffs garde les meilleures versions (voir plus bas). Chacune devient un
   fichier virtuel :
   ```
   films/<Titre> (<Année>) {tmdb-<id>}/<release> [<hash8>].mkv
   series/<Titre> (<Année>) {tmdb-<id>}/Season 01/<Titre> - S01E01 - <release> [<hash8>].mkv
   ```
   Le fichier a la bonne taille. Ses caractéristiques (codecs, résolution, HDR, pistes audio) sont déduites
   du nom de la version et exposées en attribut étendu `user.sf.probe`.
3. **Scan.** Silo, modifié, reconnaît ces fichiers et lit `user.sf.probe` au lieu d'analyser la vidéo.
   Un scan complet ne télécharge donc rien.
4. **Lecture.** Quand un client lance une lecture, Silo « arme » le fichier (`getxattr user.sf.arm`).
   sffs demande alors le lien à StreamFusion (`/playback/…`, résolu côté serveur : le client ne voit jamais
   le token AllDebrid) et transmet les octets à la volée, avec des requêtes HTTP Range.
5. **Protection.** Un fichier non armé, ou ouvert par un autre programme que ceux de `SFFS_ALLOW_COMM`,
   est refusé sans contacter le débrideur. Un `ls`, une vignette ou un antivirus ne déclenchent aucun téléchargement.
6. **Substitution.** Si la version choisie n'est pas en cache chez AllDebrid, sffs la remplace par une version
   en cache de qualité proche : une remux par une autre remux de même qualité d'abord, sinon même qualité,
   puis la plus proche. L'attribut `user.sf.served` donne alors le hash de la version réellement servie, et
   Silo analyse cette remplaçante avant de choisir comment lire. Si l'original repasse en cache, sffs y revient.
7. **Mises à jour.** Chaque minute (`SFFS_WATCH`), sffs cherche les sources ajoutées ou modifiées dans
   StreamFusion (`updated_at`), recharge seulement les titres concernés et met à jour l'arborescence **sans
   démonter**. Il demande ensuite à Silo un scan limité aux dossiers modifiés (`POST /api/v2/scan` avec `path`).
   Un rechargement complet a lieu toutes les `SFFS_RELOAD` (24 h) par sécurité.

## Sélection des versions

1. **Écartés :** doublons (même torrent proposé par plusieurs indexeurs), CAM, TS, TC, screener, R5, 3D.
   Pour un film, une version dont l'année s'écarte de plus d'un an de celle du film est aussi écartée.
2. **Langue d'abord.** Seule la meilleure langue de `SFFS_LANGS` présente pour ce titre est gardée. Elle est
   détectée dans le nom (FRENCH, TRUEFRENCH, VFF, VFQ, VFI, VF2, MULTI…) ou dans les langues relevées par
   StreamFusion. Avec `SFFS_REQUIRE_PREFERRED_HD=1`, un titre sans aucune version dans une langue préférée
   n'apparaît pas.
3. **Classement :** les versions déjà connues en cache passent d'abord (drapeau StreamFusion ou
   `known-cached.json`), puis celles qui ont le plus de seeders.
4. **Quotas par qualité :** 2160p, 1080p et 720p ; le SD et la qualité inconnue sont exclus s'il existe du HD.
   Films : `SFFS_PER_QUALITY` (5). Épisodes : `SFFS_SERIES_PER_QUALITY` (2).
   **Une remux 4K et une remux 1080p sont toujours gardées s'il en existe une** dans la bonne langue.

## Réglages de sffs

| Variable | Rôle | Défaut |
|---|---|---|
| `SF_PG_DSN` | Base Postgres de StreamFusion (lecture seule) | obligatoire |
| `SF_BASE` | URL de StreamFusion vue du serveur | obligatoire |
| `SF_STREAM_CFG` | Config Stremio chiffrée (liste des sources, état du cache) | obligatoire |
| `SF_PLAYBACK_CFG` | 1er segment des URL `/playback/` (contient le token AllDebrid) | obligatoire |
| `SFFS_MOUNT` | Point de montage | obligatoire |
| `SFFS_ALLOW_COMM` | Programmes autorisés à lire (regex sur le nom du processus). Valeur conseillée : `^(silo\|ffmpeg\|ffprobe)$` | `.` (tous) |
| `SFFS_ARM_TTL` | Durée de validité d'un armement | `6h` |
| `SFFS_LANGS` | Langues préférées, dans l'ordre | `fr` |
| `SFFS_REQUIRE_PREFERRED_HD` | Ignorer les titres sans version dans une langue préférée | `0` |
| `SFFS_PER_QUALITY` | Versions par qualité pour un film (`0` = toutes) | `5` |
| `SFFS_TMDB` | Limiter aux films listés (IDs TMDB, tests) | tous |
| `SFFS_SERIES` | Exposer aussi les séries | `0` |
| `SFFS_SERIES_PER_QUALITY` | Versions par qualité et par épisode | `2` |
| `SFFS_SERIES_TMDB` | Limiter aux séries listées | toutes |
| `SFFS_TMDB_KEY` | Clé API TMDB (obligatoire avec les séries) | |
| `SFFS_TMDB_CACHE` | Cache disque des réponses TMDB | |
| `SFFS_KNOWN_CACHE` | Sources déjà vues en cache | `$HOME/silo-test/run/known-cached.json` |
| `SFFS_WATCH` | Intervalle de détection des nouveautés (`0` = désactivé) | `1m` |
| `SFFS_RELOAD` | Rechargement complet (`0` = jamais) | `24h` |
| `SFFS_SILO_URL`, `SFFS_SILO_KEY` | API Silo pour les scans ciblés (vide = désactivé) | |
| `SFFS_SILO_FILMS`, `SFFS_SILO_SERIES` | Chemins des dossiers vus par Silo | `/mnt/sf/films`, `/mnt/sf/series` |
| `SFFS_DRY_RUN`, `SFFS_DRY_LIST` | Charger sans monter, et lister les versions retenues | |

Le service de lecture est fixé sur AllDebrid (`"service":"AD"` dans `playbackURL`, `sffs/main.go`).

## Attributs étendus

| Attribut | Contenu |
|---|---|
| `user.sf.id` | Identifiant interne de la version |
| `user.sf.probe` | Caractéristiques JSON déduites du nom de la version |
| `user.sf.arm` | Lu par Silo pour autoriser la lecture (armement) |
| `user.sf.armed` | Le fichier est-il armé ? |
| `user.sf.cached` | La version (ou une remplaçante) est-elle en cache chez AllDebrid ? |
| `user.sf.served` | Hash de la source réellement servie (remplaçante comprise) |

## Modifications de Silo (`silo/patches`)

| Patch | Rôle |
|---|---|
| 0001 | Direct play d'un flux HTTP distant via un fichier `.remote` à côté de la vidéo (premier POC, activé par `SILO_POC_REMOTE=1`) |
| 0002 | Reconnaissance des fichiers sffs (`internal/sfvirtual`) : caractéristiques lues dans `user.sf.probe`, armement avant lecture |
| 0003 | Pas de réparation d'analyse ni de scan « copy-safety » sur les fichiers sffs (ils seraient téléchargés) |
| 0004 | Réparation de l'analyse seulement pour un fichier armé, donc au moment d'une lecture |
| 0005 | Compatibilité Jellyfin : la version demandée dans PlaybackInfo est armée (Apple TV, Infuse…) |
| 0006 | Une version hors cache bascule sur une autre version du même titre |
| 0007 | Analyse de la source réellement servie (`user.sf.served`) quand sffs a substitué une version |

Silo trouve les fichiers sffs sous `SILO_SF_ROOT` (`/mnt/sf` dans le conteneur).
