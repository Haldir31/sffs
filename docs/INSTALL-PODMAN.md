# Installation avec Podman rootless (sans Docker)

Pour un serveur sans Docker ni droits root, par exemple une seedbox mutualisée. sffs tourne directement sur
l'hôte, et Silo, sa base et Redis tournent dans un pod podman. Scripts : `deploy/bin/`.

## Prérequis

- **Linux** avec FUSE 3 (`fusermount3`) utilisable par ton utilisateur. `allow_other` n'est pas nécessaire :
  le montage est seulement lu par les conteneurs de ton utilisateur.
- **Podman** 4+ en mode rootless (ce guide). Pour Docker : [INSTALL-SSD.md](INSTALL-SSD.md) ou [INSTALL-DOCKER.md](INSTALL-DOCKER.md).
- **Go** 1.21 ou plus récent. `go.mod` demande Go 1.27, téléchargé automatiquement par `go build` si besoin.
- `git`, `curl`, `openssl`, `socat` et `pg_isready` (paquet client PostgreSQL).
  `socat` sert seulement au pont vers une base StreamFusion locale non publiée (voir l'étape 3).
- Une instance **StreamFusion Reborn** fonctionnelle, configurée avec **AllDebrid**, et un accès à sa base Postgres.
- Une **clé API TMDB** (v3, gratuite) pour les séries.
- Environ 10 Go libres pour l'image Silo et la compilation. Les affiches de Silo occupent ensuite environ
  100 Go pour 50 000 films et 15 000 séries.
- Mémoire : sffs utilise environ 3 Go avec tout le catalogue, et Silo 3 à 6 Go.

## Installation pas à pas

Toutes les commandes s'exécutent avec ton utilisateur (pas root). Le dossier d'installation est
`~/sffs-stack` ; pour en changer, exporte `SFFS_STACK=/autre/chemin` avant chaque script.

### 1. Récupérer le dépôt

```bash
git clone https://github.com/Haldir31/sffs.git ~/sffs
cd ~/sffs
mkdir -p ~/sffs-stack/config
```

### 2. Configuration générale

```bash
cp deploy/config/stack.env.example ~/sffs-stack/config/stack.env
nano ~/sffs-stack/config/stack.env
```

- Choisis les **ports** (publiés seulement sur 127.0.0.1).
- `ADD_DUMMY_PRIVATE_IP=1` seulement si le serveur n'a **aucune** adresse IPv4 privée. Pour le savoir :
  `ip -4 addr` ne montre que des adresses publiques ou 100.64.x.x. C'est le cas typique d'une seedbox mutualisée.
- `SF_DB_BRIDGE` : voir l'étape 3.

### 3. Accès à la base StreamFusion

sffs lit la table `torrent_items` de StreamFusion, en **lecture seule**. Deux cas :

- **La base est joignable** (port publié, autre serveur, réseau privé) : mets `SF_DB_BRIDGE=0` et une URL
  classique dans `SF_PG_DSN` à l'étape 4. Idéalement, utilise un rôle Postgres en lecture seule :
  ```sql
  CREATE ROLE sffs LOGIN PASSWORD '…';
  GRANT CONNECT ON DATABASE streamfusion TO sffs;
  GRANT USAGE ON SCHEMA public TO sffs;
  GRANT SELECT ON torrent_items TO sffs;
  ```
- **StreamFusion tourne dans un pod podman de ce serveur, sans port Postgres publié** : mets `SF_DB_BRIDGE=1`
  et `SF_POD=<nom du pod>`. `start.sh` ouvrira un pont (socket Unix) vers le Postgres du pod, sans rien modifier
  côté StreamFusion. Le DSN utilise alors `host=<stack>/run/sfpg`.

### 4. Configuration de sffs

```bash
cp deploy/config/sffs.env.example ~/sffs-stack/config/sffs.env
chmod 600 ~/sffs-stack/config/sffs.env
```

Récupère les deux configurations StreamFusion à partir de l'URL d'installation de ton addon
(celle qui finit par `/manifest.json`) :

```bash
deploy/bin/sf-config.sh 'https://ton-streamfusion/xxxxxxxx/manifest.json'
# SF_STREAM_CFG=gAAAA…
# SF_PLAYBACK_CFG=eyJ…      <- contient ton token AllDebrid : secret
```

Remplis ensuite `sffs.env` :
- **Configurations StreamFusion :** `SF_STREAM_CFG` et `SF_PLAYBACK_CFG` (sorties ci-dessus).
- **Accès StreamFusion :** `SF_PG_DSN` (étape 3) et `SF_BASE`, l'adresse de StreamFusion vue depuis ce serveur, de préférence locale.
- **Séries :** `SFFS_TMDB_KEY`.
- **Chemins :** remplace `/home/USER/` par ton dossier personnel.
- **Clé Silo :** laisse `SFFS_SILO_KEY` vide pour l'instant (étape 9).

Le détail de chaque réglage est dans [docs/ARCHITECTURE.md](ARCHITECTURE.md#réglages-de-sffs).

### 5. Configuration de Silo et de sa base

```bash
cp deploy/config/pg.env.example   ~/sffs-stack/config/pg.env
cp deploy/config/silo.env.example ~/sffs-stack/config/silo.env
chmod 600 ~/sffs-stack/config/*.env
openssl rand -hex 24   # -> POSTGRES_PASSWORD (pg.env) ET mot de passe de DATABASE_URL (silo.env)
openssl rand -hex 32   # -> SECRET_KEY (silo.env)
```

### 6. Compiler sffs et le tester à blanc

```bash
deploy/bin/build-sffs.sh
# Essai sans montage, limité à 2 films, avec la liste des versions retenues :
( set -a; . ~/sffs-stack/config/sffs.env; set +a
  SFFS_TMDB=98,603 SFFS_SERIES=0 SFFS_DRY_RUN=1 SFFS_DRY_LIST=1 ~/sffs-stack/bin/sffs )
```

Tu dois voir des lignes `Gladiator (2000) {tmdb-98} | 2160 | remux=true | …`. Sinon, vérifie `SF_PG_DSN`
et `SFFS_LANGS`.

### 7. Construire l'image Silo modifiée

```bash
deploy/bin/build-silo.sh        # 5 à 15 minutes
```

Le script clone Silo officiel dans `~/sffs-stack/src/silo-server`, se place sur le commit testé
(`silo/BASE_COMMIT`), applique les patchs et construit l'image `localhost/silo-sffs:latest`.

### 8. Créer le pod et démarrer

```bash
deploy/bin/create-pod.sh        # pod + postgres + redis (une seule fois)
deploy/bin/start.sh             # pont (option) -> sffs -> silo
```

Le premier démarrage de sffs prend plusieurs minutes, parce qu'il remplit le cache TMDB des séries.
Les suivants prennent environ 2 minutes. Vérifie ensuite :

```bash
ls ~/sffs-stack/mnt/films | head
curl -s http://127.0.0.1:27461/api/v1/health   # 200
```

### 9. Premier réglage de Silo

1. Ouvre `http://127.0.0.1:<SILO_WEB_PORT>`, idéalement via ton reverse proxy HTTPS, et **crée le compte admin**.
2. Ajoute deux bibliothèques :
   - **Films**, type films, dossier `/mnt/sf/films`
   - **Séries**, type séries, dossier `/mnt/sf/series`

   Le premier scan dure plusieurs heures avec tout le catalogue, environ 3 h pour 200 000 versions de films.
   Les fichiers ne sont pas lus pendant le scan : leurs caractéristiques viennent de sffs.
3. Crée la clé d'API qui permet à sffs de demander des scans ciblés, puis applique-la :
   ```bash
   deploy/bin/make-silo-key.sh            # affiche SFFS_SILO_KEY=sa_…
   nano ~/sffs-stack/config/sffs.env      # colle la clé dans SFFS_SILO_KEY
   deploy/bin/redeploy.sh                 # redémarre sffs et Silo (2 à 5 min)
   ```

### 10. Redémarrage automatique

```bash
crontab -e
```
```cron
@reboot     sleep 90 && $HOME/sffs/deploy/bin/start.sh    >> $HOME/sffs-stack/run/watchdog.log 2>&1
*/5 * * * * $HOME/sffs/deploy/bin/watchdog.sh >/dev/null 2>&1
```

Pour arrêter la pile à la main sans que le watchdog la relance : `touch ~/sffs-stack/STOPPED`.

### 11. Accès depuis les clients

Mets un reverse proxy HTTPS (Caddy, nginx…) devant :
- `SILO_WEB_PORT` pour le navigateur et l'API ;
- `SILO_JF_PORT` pour les applications compatibles Jellyfin : Infuse, Swiftfin, l'application Jellyfin sur Apple TV, etc.

## Mettre à jour

| Quoi | Commandes |
|---|---|
| sffs (après `git pull`) | `deploy/bin/build-sffs.sh && deploy/bin/redeploy.sh` |
| Silo, même base | `deploy/bin/build-silo.sh && deploy/bin/redeploy.sh` |
| Silo, nouvelle version amont | `deploy/bin/build-silo.sh origin/main` : si les patchs s'appliquent, tester, puis mettre à jour `silo/BASE_COMMIT` |
| Réglages de sffs | modifier `sffs.env`, puis `deploy/bin/redeploy.sh` |

Les nouveautés de StreamFusion n'ont besoin d'aucune action : sffs les détecte chaque minute et demande
à Silo de scanner les dossiers concernés.

Voir aussi [docs/EXPLOITATION.md](EXPLOITATION.md) pour les journaux, les vérifications et le dépannage.
