# Installation avec Docker Compose (sans SSDV2)

## Prérequis

- **Docker** 23 ou plus récent (BuildKit), avec Docker Compose v2.
- `/dev/fuse` présent, et `/mnt` en propagation partagée (`findmnt -o PROPAGATION /mnt` affiche `shared`).
  C'est le cas par défaut avec systemd. Sinon : `sudo mount --make-rshared /`.
- StreamFusion joignable : idéalement sur un réseau Docker commun, sinon par adresse et port publiés.

## Pas à pas

### 1. Récupérer le dépôt et construire les images

```bash
git clone https://github.com/Haldir31/sffs.git ~/sffs && cd ~/sffs
./build-images.sh          # sffs (1 à 2 min) puis Silo (5 à 15 min)
```

### 2. Rôle en lecture seule dans la base StreamFusion

Avec un compte administrateur de la base StreamFusion :

```sql
CREATE ROLE sffs_reader LOGIN PASSWORD 'un-mot-de-passe';
ALTER ROLE sffs_reader SET default_transaction_read_only = on;
GRANT CONNECT ON DATABASE streamfusion TO sffs_reader;
GRANT USAGE ON SCHEMA public TO sffs_reader;
GRANT SELECT ON public.torrent_items TO sffs_reader;
```

### 3. Configuration

```bash
cp docker/.env.example docker/.env && chmod 600 docker/.env
deploy/bin/sf-config.sh 'https://ton-streamfusion/xxxx/manifest.json'   # -> SF_STREAM_CFG, SF_PLAYBACK_CFG
# ou, sans URL d'addon : génération automatique (clé d'API StreamFusion + clé AllDebrid)
SF_URL=http://<ip-streamfusion>:8080 SF_PUBLIC=https://ton-streamfusion SF_API_KEY=<clé> AD_TOKEN=<clé> \
  deploy/bin/sf-make-config.sh
nano docker/.env
```

Renseigne :
- **Silo :** `SILO_SECRET_KEY` et `SILO_DB_PASS` (`openssl rand -hex 32`).
- **StreamFusion :** `SF_STREAM_CFG`, `SF_PLAYBACK_CFG`, puis `SF_PG_DSN` et `SF_BASE` selon ton installation de StreamFusion.
- **Réseau :** `SF_NETWORK`, le réseau Docker de StreamFusion. Retrouve-le avec `docker network ls`.
- **Séries :** `SFFS_TMDB_KEY`.

### 4. Démarrer

```bash
docker compose -f docker/compose.yml up -d
docker logs -f sffs        # attendre « monté sur /mnt/sffs/sf »
ls /mnt/sffs/sf/films | head
```

### 5. Premier réglage de Silo

1. Ouvre `http://127.0.0.1:27461`, ou mieux ton reverse proxy HTTPS, puis crée le compte admin.
2. Ajoute les bibliothèques **Films** (`/mnt/sf/films`) et **Séries** (`/mnt/sf/series`).
3. Lance `docker/silo-key.sh` pour activer les scans ciblés des nouveautés.

Mets un reverse proxy HTTPS devant les ports `SILO_WEB_PORT` (web) et `SILO_JF_PORT` (applications Jellyfin).

## Mettre à jour

```bash
git pull && ./build-images.sh && docker compose -f docker/compose.yml up -d
```
