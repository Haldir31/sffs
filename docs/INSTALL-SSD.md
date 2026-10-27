# Installation dans SSDV2

sffs s'installe avec l'application **`silosffs`** du catalogue SSDV2, comme n'importe quelle autre application
(menu SSDV2, interface web ou `ssdv2ctl`). Elle installe toute la pile :

| Conteneur | Rôle |
|---|---|
| `silosffs` | Silo modifié (Silo officiel + patchs de `silo/patches`). Deux routes Traefik : `https://<sous-domaine>.<domaine>` (web) et `https://<sous-domaine>-jf.<domaine>` (applications Jellyfin) |
| `sffs` | Système de fichiers virtuel. Monte `/mnt/sffs` sur l'hôte (`/mnt/sffs/films`, `/mnt/sffs/series`), comme decypharr monte `/mnt/decypharr`. Pas de route Traefik |
| `db-silosffs`, `redis-silosffs`, `meili-silosffs` | Base PostgreSQL 18 + pgvector, Redis, Meilisearch |

Tous portent le label `ssdv2.app=silosffs` : la suppression de l'application les retire tous.

Le catalogue propose aussi **`silo`**, un Silo officiel classique. Les deux peuvent cohabiter. Ils ont le même
mapping de dossiers que Plex, en lecture seule : `~/Medias`, `~/local`, `/data` et `/mnt` (rslave). Un même
`silosffs` peut donc servir aussi tes bibliothèques habituelles ; hors de `/mnt/sffs`, il se comporte comme
Silo officiel.

En attendant son intégration à SSDV2, l'application est dans la branche `feat/silo` du fork
[Haldir31/ssdv2](https://github.com/Haldir31/ssdv2).

## Ce que fait l'installation

La pretask de `silosffs` :
1. **Vérifie** que StreamFusion est installé par SSDV2 (registre `ssddb`) et démarré, que `/dev/fuse` existe et
   que `/mnt` est en propagation partagée. Rien n'est créé si un contrôle échoue.
2. **Clone ce dépôt** dans `<SETTINGS_STORAGE>/docker/<user>/silosffs/sffs` (réglages `silosffs_repo` et
   `silosffs_repo_version`), puis **construit les images** `localhost/silo-sffs` (5 à 15 min la première fois) et
   `localhost/sffs` (1 à 2 min). Chaque image porte l'empreinte git de ses sources : à chaque relance de
   l'application, les sources sont mises à jour et seule une image dont les sources ont changé est reconstruite.
3. **Crée les compagnons** (base, Redis, Meilisearch) et refuse de continuer si la base a déjà été migrée par un
   Silo plus récent que l'image modifiée.
4. **Configure sffs**, sans passer par la page `/configure` de StreamFusion :
   - une **clé d'API StreamFusion dédiée** (`sffs`, table `api_keys`) ;
   - la **clé AllDebrid** de decypharr s'il est installé, demandée sinon ;
   - la **clé TMDB** de StreamFusion, demandée si elle manque ;
   - la configuration chiffrée par StreamFusion (`POST /api/config/encode`), comme le fait l'interface ;
   - le **proxy de StreamFusion** (`PROXY_URL`, en général `socks5://warp:1080`), sans lequel AllDebrid refuse
     les téléchargements depuis un serveur ;
   - un **rôle Postgres en lecture seule** dans `stremio-postgres` (`sffs_reader`, `SELECT` sur `torrent_items`).
5. **Démarre sffs** sur `/mnt/sffs`.

Les secrets sont générés ou demandés une seule fois (invites `SSDV2_PROMPT`, valeurs masquées), puis gardés dans
`<SETTINGS_STORAGE>/docker/<user>/silosffs-secrets/` (droits 0600).

La posttask crée les DNS (Cloudflare) du sous-domaine `-jf`, attend que Silo réponde, puis crée la **clé d'API Silo
de sffs** dès qu'un compte admin existe. sffs relit cette clé sans redémarrer et demande alors à Silo un scan
ciblé de chaque dossier modifié dans StreamFusion.

Testé sur un serveur SSDV2 ARM (aarch64, 4 cœurs).

## Prérequis

- SSDV2, avec **StreamFusion installé par SSDV2** et **fonctionnel** : l'addon Stremio doit déjà afficher des
  sources en cache. Sur un serveur (datacenter), AllDebrid n'accepte StreamFusion que via le proxy `warp` de
  SSDV2 ; vérifie qu'il est connecté (voir Dépannage).
- Un compte **AllDebrid** et une clé **TMDB** (déjà renseignée dans StreamFusion).
- `/dev/fuse` présent, et `/mnt` en propagation partagée (`findmnt -o PROPAGATION /mnt` affiche `shared`).
  C'est le cas par défaut, et c'est déjà nécessaire pour decypharr ou zurg.
- `git`, `curl`, `openssl` (déjà présents avec SSDV2).

## Installation

1. Installe l'application **silosffs** depuis SSDV2. Pour le sous-domaine, choisis par exemple `silosffs` ;
   pour l'authentification, choisis **aucune** : Silo gère ses propres comptes, et ses applications ne savent
   pas passer une authentification Traefik.
2. Ouvre `https://<sous-domaine>.<domaine>` et **crée le compte admin**.
3. Ajoute deux bibliothèques :
   - **Films**, type films, dossier `/mnt/sffs/films`
   - **Séries**, type séries, dossier `/mnt/sffs/series`

   Le premier scan dure plusieurs heures pour tout le catalogue. Rien n'est téléchargé pendant le scan.
4. Une fois le premier scan terminé, **relance l'application silosffs** (données conservées) : la posttask crée la
   clé d'API Silo de sffs, et les nouveautés StreamFusion apparaissent ensuite dans Silo en quelques minutes.

Clients :
- **Navigateur :** `https://<sous-domaine>.<domaine>`
- **Applications Jellyfin** (Apple TV, Infuse, Swiftfin…) : serveur `https://<sous-domaine>-jf.<domaine>`

## Au quotidien

| Action | Comment |
|---|---|
| Mettre à jour sffs et Silo modifié | relancer l'application silosffs (sources mises à jour, images reconstruites si besoin) |
| Forcer la reconstruction des images | variable `silosffs_rebuild: true` (par exemple dans `<SETTINGS_STORAGE>/vars/silosffs.yml`) |
| Redémarrer sffs | `docker restart sffs` (Silo voit le nouveau montage sans redémarrer) |
| Journaux | `docker logs -f sffs` · `docker logs -f silosffs` |
| Changer un réglage de sffs (langues, quotas…) | copier `vars/silosffs.yml` du catalogue dans `<SETTINGS_STORAGE>/vars/`, modifier `silosffs_langs`, `silosffs_per_quality`…, puis relancer l'application |
| Désinstaller | supprimer l'application silosffs (le rôle `sffs_reader` reste dans la base StreamFusion : `DROP ROLE sffs_reader` pour le retirer) |

## Ancienne installation (`ssd/install.sh`)

Jusqu'en octobre 2026, ce dépôt installait deux applications, `silosffs` et `sffs`, avec ses propres scripts
(`ssd/`), et Silo voyait les fichiers sous `/mnt/sf`. Pour passer à l'application du catalogue :

1. Sauvegarde la base : `docker exec db-silosffs pg_dump -U silo -d silo | gzip > ~/silosffs-avant-catalogue.sql.gz`
2. Supprime les applications **sffs** puis **silosffs** en gardant les données
   (`generique.sh suppression_appli sffs 0`, puis `silosffs 0`), et démonte l'ancien montage :
   `sudo umount -l /mnt/sffs/sf; sudo rmdir /mnt/sffs/sf`
3. Installe **silosffs** depuis le catalogue. Les secrets de l'ancienne installation (`sffs-secrets/`,
   `silosffs-secrets/`) sont repris.

Les chemins changent (`/mnt/sf/films` → `/mnt/sffs/films`) et la base est neuve (`postgres/` au lieu de
`db-silosffs/`, l'ancienne reste sur disque) : recrée le compte admin et les bibliothèques.

## Dépannage spécifique

| Symptôme | Cause et solution |
|---|---|
| `/mnt/sffs` vide ou « Transport endpoint is not connected » dans Silo alors qu'il est rempli sur l'hôte | Propagation : `/mnt` doit être `shared` sur l'hôte. sffs monte `/mnt` en `rshared`, Silo monte `/mnt` en `rslave` (jamais `/mnt/sffs` lui-même, qui figerait un montage mort après un redémarrage de sffs). |
| `Transport endpoint is not connected` sur l'hôte | sffs arrêté brutalement : `docker restart sffs` (le conteneur nettoie le montage mort au démarrage). |
| L'installation refuse : « base de Silo migrée par une version plus récente » | La base vient d'un Silo plus récent que `silo/BASE_COMMIT`. Attendre une mise à jour des patchs. |
| sffs refuse toutes les lectures (`refus open … processus non autorisé` dans les journaux) | `pid: host` manquant : sffs ne voit pas quel programme lit. Vérifier `docker inspect -f '{{.HostConfig.PidMode}}' sffs`. |
| Aucune version en cache (`cache movie/… : 30 sources, 0 en cache` dans `docker logs sffs`), StreamFusion journalise `NO_SERVER` | AllDebrid refuse l'adresse du serveur : le proxy `warp` est déconnecté. Test : `docker run --rm --network traefik_proxy curlimages/curl -s -x socks5h://warp:1080 https://www.cloudflare.com/cdn-cgi/trace \| grep warp` doit afficher `warp=on`. Sinon `docker restart warp`. Le conteneur peut rester « Up (unhealthy) » avec un démon WARP mort : Docker ne le relance pas tout seul. |
| Les nouveautés StreamFusion n'arrivent pas dans Silo | Pas de clé d'API : relancer l'application silosffs après avoir créé le compte admin (`silosffs-secrets/SFFS_SILO_KEY` doit exister). |

Voir aussi [EXPLOITATION.md](EXPLOITATION.md) et [ARCHITECTURE.md](ARCHITECTURE.md).
