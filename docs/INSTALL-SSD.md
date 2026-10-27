# Installation dans SSDV2

sffs et Silo s'installent comme deux applications SSDV2, par le même mécanisme que les autres
(`launch_service` → pretasks → conteneur → posttasks → registre) :

| Application | Conteneurs | Rôle |
|---|---|---|
| `sffs` | `sffs` | Système de fichiers virtuel. Monte `/mnt/sffs` sur l'hôte, comme decypharr monte `/mnt/decypharr`. Pas de route Traefik. |
| `silosffs` | `silosffs`, `db-silosffs` (PostgreSQL 18 + pgvector), `redis-silosffs` | Silo modifié, avec deux routes Traefik : `https://<sous-domaine>.<domaine>` (web) et `https://<sous-domaine>-jf.<domaine>` (applications Jellyfin) |

Ce que fait l'installation :
- **Base StreamFusion :** sffs lit directement le conteneur `stremio-postgres` de SSDV2, avec un **rôle dédié
  en lecture seule** (`sffs_reader`, droit `SELECT` sur `torrent_items` uniquement), créé automatiquement.
- **Lecture :** les liens de lecture passent par `stream-fusion:8080` sur le réseau `traefik_proxy`.
- **Secrets :** ils sont générés ou demandés une seule fois (invites `SSDV2_PROMPT`, valeurs masquées), puis gardés dans
  `<SETTINGS_STORAGE>/docker/<user>/sffs-secrets/` et `silosffs-secrets/` (droits 0600).
- **Mises à jour de SSDV2 :** aucun fichier suivi par git n'est modifié dans SSDV2, donc `git pull` reste possible.
- **Correctifs SSDV2 en attente :** comme l'interface web et `ssdv2ctl`, l'installation passe par
  `generique.sh`, qui applique d'abord les correctifs SSDV2 en attente (`patches/`). Ils peuvent modifier des
  fichiers de SSDV2, comme le catalogue ou les menus.
- **Testé** sur un serveur SSDV2 ARM (aarch64, 4 cœurs) : la construction de l'image Silo y prend moins de 10 minutes.

## Prérequis

- SSDV2 installé, avec **StreamFusion installé par SSDV2** : conteneurs `stream-fusion` et `stremio-postgres` démarrés.
- `/dev/fuse` présent, et `/mnt` en propagation partagée (`findmnt -o PROPAGATION /mnt` affiche `shared`).
  C'est le cas par défaut, et c'est déjà nécessaire pour decypharr ou zurg.
- `git`, `curl`, `openssl` (déjà présents avec SSDV2).

## Pas à pas

### 1. Récupérer le dépôt

Avec l'utilisateur SSDV2 (pas root) :

```bash
git clone https://github.com/Haldir31/sffs.git ~/sffs
cd ~/sffs
```

Le dépôt doit rester à cet endroit : SSDV2 y reconstruit les images lors des réinstallations.

### 2. Rien à préparer côté StreamFusion

sffs génère lui-même sa configuration StreamFusion, sans passer par la page `/configure` :
- il crée une **clé d'API StreamFusion dédiée** (`sffs`, table `api_keys`) ;
- il reprend la **clé AllDebrid** de decypharr s'il est installé, et la demande sinon ;
- il reprend la **clé TMDB** de StreamFusion ;
- il trouve l'**adresse publique** de StreamFusion dans ses labels Traefik ;
- il fait chiffrer la configuration par StreamFusion (`POST /api/config/encode`), comme le fait l'interface.

Pour utiliser plutôt une URL d'addon existante : `ssd/install.sh --manifest '<url>/manifest.json'`.

### 3. Lancer l'installation

```bash
ssd/install.sh --subdomain silosffs --auth aucune
```

Toutes les options : `--subdomain`, `--auth`, `--manifest`, `--only sffs|silo`, `--files`. Sans option,
les questions sont posées pendant l'installation.

Le script :
1. **Vérifie** Docker, `/dev/fuse`, StreamFusion et la propagation de `/mnt`.
2. **Dépose les définitions** d'applications dans SSDV2 : `vars/` dans `<SETTINGS_STORAGE>/vars/`, `pretasks/`
   et `posttasks/` dans les sources SSDV2.
3. **Installe sffs.** Il génère la configuration StreamFusion (étape 2), crée le rôle `sffs_reader`, construit
   l'image (1 à 2 min) et démarre le conteneur. Il attend ensuite que `/mnt/sffs` soit monté. Au premier démarrage,
   sffs interroge TMDB pour toutes les séries, ce qui prend 10 à 20 minutes.
4. **Installe silosffs.** Il demande le **sous-domaine** et l'**authentification** : choisis `aucune`, car Silo
   gère ses propres comptes et ses applications ne passent pas une authentification Traefik. Il construit ensuite
   l'image Silo (5 à 15 min la première fois), puis crée la base, Redis et le conteneur. Avec Cloudflare,
   les DNS du sous-domaine et du sous-domaine `-jf` sont créés automatiquement.

### 4. Premier réglage de Silo

1. Ouvre `https://<sous-domaine>.<domaine>` et **crée le compte admin**.
2. Ajoute deux bibliothèques :
   - **Films**, type films, dossier `/mnt/sf/films`
   - **Séries**, type séries, dossier `/mnt/sf/series`

   Le premier scan dure plusieurs heures pour tout le catalogue. Rien n'est téléchargé pendant le scan.
3. Active les scans ciblés des nouveautés StreamFusion :
   ```bash
   ssd/silo-key.sh
   ```
   Le script crée une clé d'API Silo pour sffs, l'enregistre dans les secrets et redéploie sffs.

### 5. Clients

- **Navigateur :** `https://<sous-domaine>.<domaine>`
- **Applications Jellyfin** (Apple TV, Infuse, Swiftfin…) : serveur `https://<sous-domaine>-jf.<domaine>`

## Au quotidien

| Action | Commande |
|---|---|
| Mettre à jour sffs et/ou Silo après `git pull` | `ssd/update.sh [sffs\|silo\|all]` |
| Redémarrer | `docker restart sffs` (Silo voit le nouveau montage sans redémarrer) |
| Journaux | `docker logs -f sffs` · `docker logs -f silosffs` |
| Changer un réglage de sffs (langues, quotas…) | modifier `<SETTINGS_STORAGE>/vars/sffs.yml` (`sffs_langs`, `sffs_per_quality`…), puis `ssd/update.sh sffs` |
| Désinstaller | `ssd/uninstall.sh` (données et secrets conservés) |

Après une mise à jour de SSDV2 qui aurait nettoyé les fichiers non suivis, relance `ssd/install.sh --files`
pour remettre les définitions.

## Dépannage spécifique

| Symptôme | Cause et solution |
|---|---|
| `/mnt/sffs` vide dans Silo alors qu'il est rempli sur l'hôte | Propagation : `/mnt` doit être `shared` sur l'hôte. Silo monte `/mnt/sffs` en `rslave`. |
| `Transport endpoint is not connected` sur l'hôte | sffs arrêté brutalement : `docker restart sffs` (le conteneur nettoie le montage mort au démarrage). |
| sffs refuse toutes les lectures (`refus open … processus non autorisé` dans les journaux) | `pid: host` manquant : sffs ne voit pas quel programme lit. Vérifier `docker inspect -f '{{.HostConfig.PidMode}}' sffs`. |
| `sf-config.sh` : « aucun flux /playback/ » | URL du manifest incorrecte, StreamFusion injoignable depuis l'hôte, ou film de test sans source : relancer avec un autre identifiant IMDb (`deploy/bin/sf-config.sh <url> tt…`). |

Voir aussi [EXPLOITATION.md](EXPLOITATION.md) et [ARCHITECTURE.md](ARCHITECTURE.md).
