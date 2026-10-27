# sffs : notes pour Claude Code

Réponses en français. Lire aussi `README.md` et `docs/` (ARCHITECTURE, INSTALL-SSD, EXPLOITATION).

## Ce que fait le projet

StreamFusion (addon Stremio, base Postgres de torrents AllDebrid) → **sffs** (Go, go-fuse + pgx)
présente les films/séries comme des fichiers virtuels → **Silo modifié** (silosffs) les scanne et
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

`silo/patches/0001…0006` sur `silo/BASE_COMMIT` (Silo officiel), appliqués par
`build-images.sh silo` (clone → checkout BASE → `git am` → image `localhost/silo-sffs`).
Actifs seulement sous `SILO_SF_ROOT` ; le reste se comporte comme Silo officiel.
Pour modifier le fork : appliquer les patchs dans un clone, committer, puis
`git format-patch BASE..HEAD` vers `silo/patches/`. Ne jamais proposer ces patchs à l'amont.

## Montages (point délicat)

sffs monte dans **`/mnt/sffs/sf`** ; Silo monte le **dossier parent** `/mnt/sffs` en `rslave`
(→ `/mnt/sf` dans silosffs). Monter le point FUSE lui-même fige un montage mort après un
redémarrage de sffs (« Transport endpoint is not connected »). `/mnt` doit être `shared`.
L'entrypoint ne démonte que les montages FUSE morts, jamais le bind Docker.

## Installation SSDV2 (`ssd/`)

- Applications `sffs` et `silosffs` : `vars/` copiés dans `<STORAGE>/vars/`, pretasks/posttasks
  dans `<SOURCE>/includes/dockerapps/` (non suivis, exclus de git). Lancement par
  `generique.sh launch_service`, suppression par `generique.sh suppression_appli <app> [0|1]`
  (`ssdv2ctl` refuse les applications hors catalogue).
- Prérequis vérifiés avant toute création : StreamFusion enregistré dans `ssddb` (statut 2) et
  démarré ; sffs seul exige silosffs. Ordre : silosffs puis sffs.
- Secrets en fichiers 0600 dans `<STORAGE>/docker/<user>/<app>-secrets/` (format StreamFusion).
- `ssd/update.sh [sffs|silo|all]` reconstruit et redéploie (migre aussi l'ancien montage).
- `ssd/silo-key.sh` après la création du compte admin Silo : attend la fin des scans avant de
  redéployer sffs.

## Conventions

- **Jamais** de nom ni d'e-mail personnel dans le code, les commits ou les fichiers. Identité git :
  `haldir <haldir@localhost>` (vérifier `git config user.email` avant de committer).
- Jamais de secret dans le dépôt (clés AllDebrid/TMDB/API, mots de passe, URL d'addon chiffrée).
- Tester sur un vrai serveur SSDV2 avant de pousser ; demander avant de toucher à la production
  (StreamFusion, warp, Plex…) ou de supprimer des données.
- Commits en français ; pousser après chaque changement testé.
