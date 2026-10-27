# Installation dans SSDV2

*English: [en/INSTALL-SSD.md](en/INSTALL-SSD.md)*

Deux applications du catalogue SSDV2 :

| Application | Rôle |
|---|---|
| `silo` | Silo officiel, sans modification. Mêmes dossiers que Plex, en lecture seule : `~/Medias`, `~/local`, `/data`, `/mnt` (rslave) |
| `sffs` | Le système de fichiers virtuel (conteneur `sffs`, monté sur `/mnt/sffs`), plus le support de sffs dans `silo` |

L'application **`sffs`** couvre deux cas :
- **Silo est déjà installé** : il est patché sur place. Sa base, ses comptes, ses bibliothèques et son
  sous-domaine sont conservés ; seule l'image change (Silo officiel + patchs de `silo/patches`).
- **Silo n'est pas installé** : sffs demande le sous-domaine de Silo, s'installe, puis installe `silo`.

Dans les deux cas, sffs est démarré avant Silo, et `<SETTINGS_STORAGE>/vars/silo.yml` est écrit : c'est la
définition de `silo` avec l'image modifiée et `SILO_SF_ROOT=/mnt/sffs`. SSDV2 l'utilise à la place de celle du
catalogue, donc les relances de `silo` gardent le support de sffs.

Hors de `/mnt/sffs`, le Silo modifié se comporte comme Silo officiel : un même Silo sert les bibliothèques
habituelles (`~/Medias/Films`…) et celles de sffs (`/mnt/sffs/films`, `/mnt/sffs/series`).

En attendant leur intégration à SSDV2, ces applications sont dans la branche `feat/silo` du fork
[Haldir31/ssdv2](https://github.com/Haldir31/ssdv2).

## Ce que fait l'installation de sffs

1. **Vérifie** que StreamFusion est installé par SSDV2 (registre `ssddb`) et démarré, que `/dev/fuse` existe et
   que `/mnt` est en propagation partagée. Rien n'est créé si un contrôle échoue.
2. **Clone ce dépôt** dans `<SETTINGS_STORAGE>/docker/<user>/sffs/src` (réglages `sffs_repo`, `sffs_repo_version`).
3. **Images** `localhost/sffs` et `localhost/silo-sffs` : chaque image porte l'empreinte git de ses sources
   (étiquette `sffs.tree`). Une image à jour est gardée ; sinon elle est **téléchargée** depuis
   `ghcr.io/haldir31` (construite par GitHub Actions, amd64 et arm64) si elle correspond aux sources, et
   **construite** sur le serveur sinon (5 à 15 min pour Silo ; forcer avec `sffs_build_local: true`).
4. **Contrôle de version** : si la base de Silo a déjà été migrée par une version plus récente que l'image
   modifiée, l'installation s'arrête sans rien changer.
5. **Configure sffs**, sans passer par la page `/configure` de StreamFusion :
   - une **clé d'API StreamFusion dédiée** (`sffs`, table `api_keys`) ;
   - la **clé AllDebrid** de decypharr s'il est installé, demandée sinon ;
   - la **clé TMDB** de StreamFusion, demandée si elle manque ;
   - la configuration chiffrée par StreamFusion (`POST /api/config/encode`), comme le fait l'interface ;
   - le **proxy de StreamFusion** (`PROXY_URL`, en général `socks5://warp:1080`), sans lequel AllDebrid refuse
     les téléchargements depuis un serveur ;
   - un **rôle Postgres en lecture seule** dans `stremio-postgres` (`sffs_reader`, `SELECT` sur `torrent_items`).
6. **Écrit `vars/silo.yml`** (image modifiée). Un `vars/silo.yml` personnel est gardé en `silo.yml.avant-sffs`
   et sert de base.
7. **Démarre sffs**, puis **installe ou redéploie `silo`** par SSDV2 (sa pretask : base, Redis, Meilisearch,
   GPU ; sa posttask : DNS, compte admin).
8. Crée la **clé d'API Silo de sffs** dès qu'un compte admin existe. sffs la relit sans redémarrer et demande
   alors à Silo un scan ciblé de chaque dossier modifié dans StreamFusion.

Secrets : `<SETTINGS_STORAGE>/docker/<user>/sffs-secrets/` (droits 0600), générés ou demandés une seule fois.

## Prérequis

- SSDV2, avec **StreamFusion installé par SSDV2** et **fonctionnel** : l'addon Stremio doit déjà afficher des
  sources en cache. Sur un serveur (datacenter), AllDebrid n'accepte StreamFusion que via le proxy `warp` de
  SSDV2 ; vérifie qu'il est connecté (voir Dépannage).
- Un compte **AllDebrid** et une clé **TMDB** (déjà renseignée dans StreamFusion).
- `/dev/fuse` présent, et `/mnt` en propagation partagée (`findmnt -o PROPAGATION /mnt` affiche `shared`).
  C'est le cas par défaut, et c'est déjà nécessaire pour decypharr ou zurg.

## Installation

1. Installe l'application **sffs** depuis SSDV2. Si Silo n'est pas installé, elle demande son sous-domaine
   (l'authentification Traefik de Silo est réglée sur `aucune` : Silo gère ses comptes, et les applications
   Jellyfin ne savent pas passer une authentification Traefik).
2. **Compte admin** : un Silo nouveau le reçoit automatiquement, avec l'identifiant et le mot de passe de
   SSDV2 (comme les autres applications du catalogue). Si le mot de passe SSDV2 fait moins de 8 caractères
   (minimum de Silo), crée le compte dans l'interface web, puis relance l'application sffs.
3. **Bibliothèques** : sffs crée lui-même **Films** (`/mnt/sffs/films`) et **Séries** (`/mnt/sffs/series`),
   métadonnées en français, dès que sa clé d'API Silo existe et que `/mnt/sffs` est monté (1 à 20 minutes
   après l'installation selon le cache TMDB). Une seule fois : une bibliothèque supprimée n'est pas recréée.

   Le premier scan dure plusieurs heures pour tout le catalogue. Rien n'est téléchargé pendant le scan.
   Si sffs redémarre pendant ce scan, Silo voit les dossiers disparaître une minute et marque la bibliothèque
   injoignable (sans rien supprimer) : sffs relance alors lui-même son scan dans les 5 minutes.
4. Les nouveautés StreamFusion apparaissent ensuite dans Silo en quelques minutes.

Clients :
- **Navigateur :** `https://<sous-domaine>.<domaine>`
- **Applications Jellyfin** (Apple TV, Infuse, Swiftfin…) : serveur `https://<sous-domaine>-jf.<domaine>`

## Options de l'application sffs

Pour changer une option : copier `includes/dockerapps/vars/sffs.yml` du catalogue SSDV2 dans
`<SETTINGS_STORAGE>/vars/sffs.yml`, la modifier, puis relancer l'application sffs. L'arborescence est mise à
jour sur place, et Silo voit les changements à son prochain scan.

| Option | Défaut | Effet |
|---|---|---|
| `sffs_langs` | `fr` | Langues voulues, par ordre de préférence (`fr,en`…) |
| `sffs_per_quality` | `5` | Versions gardées par qualité (2160p, 1080p, 720p…) pour un film |
| `sffs_series` | `1` | `0` : films seulement |
| `sffs_series_per_quality` | `2` | Versions par qualité et par épisode |
| `sffs_remux` | `1` | `0` : pas de REMUX (fichiers plus petits, moins de 4K de qualité disque) |
| `sffs_light` | `0` | `1` : proposer aussi les encodages légers (HDLight, 4KLight, mHD…) |
| `sffs_cached_only` | `0` | `1` : **seulement les versions déjà en cache chez AllDebrid**. Voir ci-dessous |
| `sffs_repo`, `sffs_repo_version` | ce dépôt, `main` | Sources de sffs et des patchs Silo |
| `sffs_registry` | `ghcr.io/haldir31` | Registre des images ; vide : construction sur le serveur |
| `sffs_build_local` | `false` | `true` : toujours construire les images sur le serveur |
| `sffs_alldebrid_key` | | Clé AllDebrid, si decypharr n'est pas installé et qu'on ne veut pas de question |
| `sffs_debrid_proxy_override` | proxy de StreamFusion | Proxy des lectures chez AllDebrid ; `''` pour aucun |

Toujours appliqué, sans option : au moins un REMUX 2160p et un REMUX 1080p quand il en existe (si
`sffs_remux` vaut `1`), et jamais de CAM, screener, 3D ni d'image disque (ISO, BDMV, COMPLETE/Full BluRay…), que Silo ne sait pas lire.

### Mode « en cache seulement » (`sffs_cached_only: '1'`)

sffs ne propose que les versions vues en cache chez AllDebrid : celles que StreamFusion a marquées disponibles
(colonne `availability`), et celles que sffs a lui-même vues en cache lors d'une lecture. Un titre sans aucune
version en cache n'apparaît pas.

- **Avantage :** toute version proposée démarre sans attente de téléchargement chez AllDebrid.
- **Inconvénient :** beaucoup moins de titres. Mesure sur une base de 1,2 million de torrents : environ 16 700
  films (36 400 versions) au lieu de 43 250 (162 300 versions).
- **Au fil du temps :** quand une version passe en cache, la surveillance la voit et fait apparaître son titre
  dans la minute. Le cache d'AllDebrid peut aussi s'être vidé depuis la vérification de StreamFusion : à la
  lecture, sffs remplace alors la version par une source en cache de même qualité, comme en mode normal.
- **Activer ou désactiver** ajoute ou retire des milliers de titres : Silo les marque manquants (ou les
  ajoute) à ses scans suivants, sans toucher à l'historique de lecture des titres qui restent.

## Au quotidien

| Action | Comment |
|---|---|
| Mettre à jour sffs et Silo modifié | relancer l'application sffs (sources mises à jour, images téléchargées ou reconstruites si besoin, Silo redéployé) |
| Redémarrer sffs | `docker restart sffs` (Silo voit le nouveau montage sans redémarrer) |
| Journaux | `docker logs -f sffs` · `docker logs -f silo` |
| Changer une option de sffs | voir [Options de l'application sffs](#options-de-lapplication-sffs) |
| Retirer sffs | supprimer l'application sffs : `vars/silo.yml` est retiré, et silo repasse à l'image officielle à sa prochaine relance (sa base est gardée). Le rôle `sffs_reader` reste dans la base StreamFusion (`DROP ROLE sffs_reader`) |

**Sauvegarde** : l'application `silo` sauvegarde sa base chaque nuit (`pg_dump`, minuteur systemd
`silo-backup.timer`) dans `<SETTINGS_STORAGE>/docker/<user>/silo/backups/`, 7 jours gardés. Garde aussi
`silo-secrets/SILO_SECRET_KEY` : sans elle, une base restaurée ne se relit pas. Restauration :
`docker exec -i db-silo pg_restore -U silo -d silo --clean --if-exists < fichier.dump`.

**Mises à jour de Silo** : chaque semaine, GitHub Actions reporte les patchs sur la dernière version de Silo
officiel, lance les tests et ouvre une pull request sur ce dépôt ; une fois fusionnée, les images sont
reconstruites et la prochaine relance de sffs les déploie.

Ne mets pas à jour `silo` vers une image officielle plus récente tant que sffs est installé : la base serait
migrée plus loin que l'image modifiée. Les mises à jour de Silo passent par sffs.

## Dépannage spécifique

| Symptôme | Cause et solution |
|---|---|
| `/mnt/sffs` vide ou « Transport endpoint is not connected » dans Silo alors qu'il est rempli sur l'hôte | Propagation : `/mnt` doit être `shared` sur l'hôte. sffs monte `/mnt` en `rshared`, Silo monte `/mnt` en `rslave` (jamais `/mnt/sffs` lui-même, qui figerait un montage mort après un redémarrage de sffs). |
| `Transport endpoint is not connected` sur l'hôte | sffs arrêté brutalement : `docker restart sffs` (le conteneur nettoie le montage mort au démarrage). |
| L'installation refuse : « Silo est plus récent que l'image modifiée » | La base vient d'un Silo plus récent que `silo/BASE_COMMIT`. Attendre une mise à jour des patchs. |
| sffs refuse toutes les lectures (`refus open … processus non autorisé` dans les journaux) | `pid: host` manquant : sffs ne voit pas quel programme lit. Vérifier `docker inspect -f '{{.HostConfig.PidMode}}' sffs`. |
| Aucune version en cache (`cache movie/… : 30 sources, 0 en cache` dans `docker logs sffs`), StreamFusion journalise `NO_SERVER` | AllDebrid refuse l'adresse du serveur : le proxy `warp` est déconnecté. Test : `docker run --rm --network traefik_proxy curlimages/curl -s -x socks5h://warp:1080 https://www.cloudflare.com/cdn-cgi/trace \| grep warp` doit afficher `warp=on`. Sinon `docker restart warp`. Le conteneur peut rester « Up (unhealthy) » avec un démon WARP mort : Docker ne le relance pas tout seul. |
| Les nouveautés StreamFusion n'arrivent pas dans Silo | Pas de clé d'API : relancer l'application sffs après avoir créé le compte admin (`sffs-secrets/SFFS_SILO_KEY` doit exister). |
| Bibliothèque marquée injoignable (`dead_root`), scan en erreur | sffs a redémarré pendant un scan. Rien n'est supprimé, et sffs relance le scan dans les 5 minutes (`silo : bibliothèque … scan relancé` dans ses journaux). |

Voir aussi [EXPLOITATION.md](EXPLOITATION.md) et [ARCHITECTURE.md](ARCHITECTURE.md).
