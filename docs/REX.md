# sffs comme source de rex (applications Plex)

[rex](https://github.com/debridmediamanager/rex) se présente aux applications Plex officielles
comme un serveur Plex qui ne possède aucun fichier. sffs peut l'alimenter directement : même
catalogue StreamFusion, même sélection de versions, lecture chez AllDebrid seulement au moment
d'une lecture. Le montage FUSE et Silo restent disponibles en parallèle.

> **Expérimental.** rex est un projet indépendant, ni fait ni approuvé par Plex. Son auteur
> conseille de le déclarer sur un compte Plex gratuit créé pour lui, puis de le partager avec
> votre compte habituel.

## Fonctionnement

- **Catalogue.** sffs enregistre une source dans rex (`PUT /rex/sources/<nom>`), envoie tout le
  catalogue au démarrage (`/rex/feed/<nom>/snapshot`, par paquets de 300), puis seulement les
  dossiers modifiés (surveillance de StreamFusion et rechargement complet). Chaque fichier est
  placé par son identifiant TMDB (film, ou série avec saison et épisode). rex regroupe les
  versions d'un même titre.
- **Octets.** sffs écoute sur `SFFS_REX_LISTEN` (127.0.0.1 par défaut) et exige un jeton (`Bearer`)
  que rex reçoit à l'enregistrement. Une demande de rex est relayée chez AllDebrid avec la même
  plage (`Range`), par `SFFS_DEBRID_PROXY` si défini. Une version qui n'est plus en cache est
  remplacée par une version en cache, comme pour Silo.
- **Ordre des versions.** Une application Plex lance la première version d'un titre : le serveur
  ne choisit pas, il convertit celle qu'on lui demande. sffs envoie donc les versions de chaque
  titre de la plus compatible (1080p, puis 720p, puis 4K ; H.264 avant HEVC ; SDR ; son AC3/E-AC3/AAC
  avant DTS/TrueHD) à la moins compatible, par vagues (rex range les versions dans leur ordre de
  création). `SFFS_REX_ORDER=quality` met la meilleure qualité en tête (4K REMUX), pour des
  lecteurs qui la lisent sans conversion. L'ordre est fixé à la création du titre dans rex.
- **Pas de garde-fou.** rex n'analyse aucun fichier : seule une lecture (ou une conversion) lit
  des octets.
- **Infos techniques.** rex exige un résultat ffprobe par fichier. Au départ, sffs le fabrique
  d'après ce que StreamFusion déduit du nom de la release (codecs, résolution, HDR, langues), sans
  durée ni pistes réelles. À la première lecture, sffs analyse la source réellement servie avec
  `ffprobe` (formats, pistes, sous-titres, chapitres, durée), garde le résultat dans
  `SFFS_REX_STATE/probes/` et met le fichier à jour dans rex, sur la même clé (historique de
  visionnage conservé). La taille n'est envoyée qu'une fois connue exactement.

## Mise en place

1. rex **nightly 2026.10.25 ou plus récent** (API d'alimentation), déclaré sur un compte Plex :
   avant la déclaration, rex ne récupère ni titres ni affiches, et ne regroupe pas les versions
   d'un film (il le fait dès la déclaration).
2. `ffprobe` installé à côté de sffs (ou `SFFS_FFPROBE`).
3. Dans la configuration de sffs :

   ```sh
   SFFS_REX_URL=http://127.0.0.1:32400      # rex
   SFFS_REX_LISTEN=127.0.0.1:32545          # serveur d'octets de sffs
   # SFFS_REX_BYTES_URL=http://sffs:32545   # si rex le joint autrement (conteneur)
   # SFFS_REX_SOURCE=sffs                    # nom de la source dans rex
   # SFFS_REX_STATE=~/.cache/sffs-rex        # jeton et analyses
   ```

4. Un reverse proxy sur la machine de rex doit **bloquer `/rex/`** : rex accepte l'API
   d'alimentation de toute adresse locale.

Limiter le catalogue pendant un essai : `SFFS_TMDB` (films) et `SFFS_SERIES_TMDB` (séries). rex
identifie quelques centaines de fichiers par minute au premier remplissage.

## Variables

| Variable | Défaut | Rôle |
|---|---|---|
| `SFFS_REX_URL` | vide (coupé) | Adresse de rex |
| `SFFS_REX_SOURCE` | `sffs` | Nom de la source dans rex |
| `SFFS_REX_LISTEN` | `127.0.0.1:32545` | Écoute du serveur d'octets |
| `SFFS_REX_BYTES_URL` | `http://<SFFS_REX_LISTEN>` | Adresse du serveur d'octets vue par rex |
| `SFFS_REX_TOKEN` / `SFFS_REX_TOKEN_FILE` | créé dans `SFFS_REX_STATE/token` | Jeton exigé par le serveur d'octets |
| `SFFS_REX_STATE` | `~/.cache/sffs-rex` | Jeton et analyses ffprobe |
| `SFFS_REX_ORDER` | `compat` | Ordre des versions : `compat` ou `quality` |
| `SFFS_REX_PROBE` | `1` | `0` : pas d'analyse à la première lecture |
| `SFFS_REX_FORCE` | vide | `1` : envoi complet autorisé à retirer plus de la moitié des fichiers |
| `SFFS_FFPROBE` | `ffprobe` | Binaire ffprobe |

## Limites connues

- Avant sa première lecture, un fichier n'a ni durée ni liste réelle de pistes : une application
  Plex peut choisir une conversion inutile, ou mal afficher la barre de progression.
- Une version remplacée (hors cache) est analysée telle qu'elle est servie ; si la remplaçante
  change ensuite, l'analyse est refaite à la lecture suivante.
- Un fichier retiré de sffs part à la corbeille de rex, comme dans Plex.
