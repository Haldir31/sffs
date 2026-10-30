# sffs : notes pour Claude Code

Réponses en français. Lire aussi `README.md` et `docs/` (ARCHITECTURE, INSTALL-SSD, EXPLOITATION).

## Ce que fait le projet

StreamFusion (addon Stremio, base Postgres de torrents AllDebrid) → **sffs** (Go, go-fuse + pgx)
présente les films/séries comme des fichiers virtuels → **Silo modifié** (application `silo` de SSDV2 patchée par l'application `sffs`, image `localhost/silo-sffs`) les scanne et
les lit. Les octets ne sont lus chez AllDebrid qu'au moment d'une lecture.

- **Garde-fou** : un fichier ne s'ouvre que s'il a été « armé » (getxattr `user.sf.arm`), ce que
  seul le Silo patché fait au lancement d'une lecture, et seulement par les processus de
  `SFFS_ALLOW_COMM` (`silo|ffmpeg|ffprobe`). Plex/Jellyfin/Silo officiel ne peuvent rien lire :
  c'est voulu (un scan complet déclencherait des milliers de requêtes AllDebrid).
- **Attributs** : `user.sf.id`, `probe` (JSON tiré de StreamFusion, remplace ffprobe au scan),
  `arm`, `armed`, `cached`, `served` (hash de la source réellement servie, remplaçante comprise).
- **Sélection** : `selectVersions` (langues, quota par qualité, au moins un REMUX 2160p et un
  REMUX 1080p s'ils existent) ; une version hors cache est remplacée par une version en cache.
- **Mises à jour** : `SFFS_WATCH` (1 min) lit les nouveautés de StreamFusion et demande à Silo un
  scan ciblé par dossier (`POST /api/v2/scan`, clé d'API admin `SFFS_SILO_KEY`) ;
  `SFFS_RELOAD` (24 h) recharge tout, arbre FUSE modifié sur place.
- **Téléchargements** : AllDebrid refuse (503 / `NO_SERVER`) les adresses de datacenter.
  StreamFusion passe par le proxy WARP ; sffs aussi via `SFFS_DEBRID_PROXY` (SSDV2 : repris de
  `PROXY_URL` de StreamFusion). Symptôme d'un WARP mort : « 0 en cache » partout.

## Silo modifié

`silo/patches/0001…0009` sur `silo/BASE_COMMIT` (Silo officiel), appliqués par
`build-images.sh silo` (clone → checkout BASE → `git am` → image `localhost/silo-sffs`).
Actifs seulement sous `SILO_SF_ROOT` ; le reste se comporte comme Silo officiel (sauf 0009, correctif de
performance du scan des séries, valable pour toutes les bibliothèques).
Pour modifier le fork : appliquer les patchs dans un clone, committer (titres « sffs : … », un
comportement par patch), puis `git format-patch BASE..HEAD` vers `silo/patches/` et renommer les
fichiers sans accents (`NNNN-nom-court.patch`, seul l'ordre compte). Ne jamais proposer ces patchs à l'amont.

## Montages (point délicat)

SSDV2 : sffs monte `/mnt:/mnt:rshared` (comme decypharr) et pose son FUSE sur **`/mnt/sffs`** ;
Silo monte `/mnt:/mnt:ro,rslave` (comme Plex). Mêmes chemins partout : `/mnt/sffs/films`.
Ne jamais monter le point FUSE lui-même dans Silo : après un redémarrage de sffs, il resterait un
montage mort (« Transport endpoint is not connected »). Il faut monter un dossier parent ordinaire.
`/mnt` doit être `shared`. L'entrypoint démonte les montages FUSE morts sur `SFFS_MOUNT` (et rien d'autre).
Compose/Podman gardent l'ancienne disposition (`/mnt/sffs/sf`, vu comme `/mnt/sf` dans Silo).

## CI (GitHub Actions)

- `tests.yml` : sffs (gofmt, vet, test) + Silo patché (build, vet, test des paquets patchés), appelé par
  `images.yml` avant toute publication (ghcr.io/haldir31, amd64 + arm64).
- `silo-upstream.yml` (lundi) : patchs appliqués sur Silo `main` et testés -> pull request qui met à jour
  `silo/patches` et `BASE_COMMIT` (bot github-actions) ; échec = patchs à reporter à la main.

## Installation SSDV2 (applications `silo` et `sffs` du catalogue)

- Définies dans le fork SSDV2 (`~/ssdv2-fork`, branche `feat/silo`) : `silo` = Silo officiel (mapping de
  Plex : `~/Medias`, `~/local`, `/data`, `/mnt` rslave, en lecture seule) ; `sffs` = conteneur sffs + support
  dans silo. Plus de scripts `ssd/` dans ce dépôt.
- `sffs` patche un `silo` existant sur place (base gardée) ou l'installe s'il est absent (sous-domaine
  demandé dans la pretask). La pretask écrit `<STORAGE>/vars/silo.yml` (marqueur `# sffs-patch`, image
  `localhost/silo-sffs`, `SILO_SF_ROOT=/mnt/sffs`) ; la posttask lance `generique.sh launch_service silo`.
  Suppression de sffs (`functions.sh`) : démonte `/mnt/sffs`, retire ce fichier.
- Images : clone dans `<STORAGE>/docker/<user>/sffs/src` ; étiquette `sffs.tree` = `HEAD:sffs` / `HEAD:silo`.
  Image locale à jour gardée, sinon `ghcr.io/haldir31/<image>:latest` si son étiquette correspond (publiée
  par `.github/workflows/images.yml`), sinon `build-images.sh`.
- Refus si la base de silo est migrée plus loin que l'image (`sffs.silo.migration` vs `goose_db_version`).
  `.github/workflows/silo-upstream.yml` vérifie chaque semaine que les patchs s'appliquent sur Silo `main`.
- Clé d'API Silo de sffs : créée par la posttask dès qu'un admin existe, dans `sffs-secrets/SFFS_SILO_KEY`,
  lue via `SFFS_SILO_KEY_FILE` (sans redémarrage).
- Sur un SSDV2 sans ces fichiers (ex. `master`, comme ce serveur), copier `vars|pretasks|posttasks/{silo,sffs}.yml`
  dans `<SOURCE>/includes/dockerapps/` (non suivis, ajoutés à `.git/info/exclude`).

## Conventions

- **Jamais** de nom ni d'e-mail personnel dans le code, les commits ou les fichiers. Identité git :
  `Haldir31 <53797644+Haldir31@users.noreply.github.com>`, l'adresse anonyme GitHub du compte : les commits
  pointent vers le profil sans révéler d'e-mail (vérifier `git config user.email` avant de committer).
- Jamais de secret dans le dépôt (clés AllDebrid/TMDB/API, mots de passe, URL d'addon chiffrée).
- Tester sur un vrai serveur SSDV2 avant de pousser ; demander avant de toucher à la production
  (StreamFusion, warp, Plex…) ou de supprimer des données.
- Commits en français ; pousser après chaque changement testé.
