// sffs : système de fichiers virtuel (FUSE, lecture seule) qui présente les torrents de films
// de la base StreamFusion comme une bibliothèque Silo :
//
//	<montage>/films/<Titre> (<Année>) {tmdb-<id>}/<release> [<hash8>].<ext>
//
// Rien n'est stocké sur disque. Les métadonnées viennent de la base StreamFusion (lecture seule).
// Les octets ne sont lus chez le débrideur qu'à la lecture, via l'URL /playback de StreamFusion
// (résolue côté serveur : le client ne voit jamais l'URL, qui contient le token AllDebrid).
//
// Garde-fou : un fichier n'est lisible que s'il a été « armé » (getxattr user.sf.arm), ce que
// fait le fork Silo au lancement d'une lecture. Toute autre ouverture (vignettes, analyses en
// tâche de fond…) est refusée sans contacter le débrideur.
//
// Attributs étendus exposés : user.sf.id, user.sf.probe (JSON des caractéristiques), user.sf.arm,
// user.sf.served (hash de la source réellement lue : user.sf.probe ne décrit pas une remplaçante).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------- configuration

type config struct {
	dsn         string
	base        string // http://127.0.0.1:28080
	playbackCfg string // 1er segment base64 des URL /playback (config StreamFusion)
	streamCfg   string // config Stremio chiffrée (gAAAA…) : liste des sources, pour savoir ce qui est en cache
	mount       string
	tmdb        []int64 // vide = tous les films
	allowComm   *regexp.Regexp
	armTTL      time.Duration
	langs       []string // langues préférées, dans l'ordre (SFFS_LANGS, défaut fr)
	perQuality  int      // releases gardées par qualité (SFFS_PER_QUALITY, défaut 5, 0 = toutes)
	requireHD   bool     // SFFS_REQUIRE_PREFERRED_HD=1 : écarte les films sans version HD dans une langue préférée

	series           bool          // SFFS_SERIES=1 : expose aussi les séries sous series/
	seriesTmdb       []int64       // SFFS_SERIES_TMDB : séries limitées à ces IDs (vide = toutes)
	seriesPerQuality int           // SFFS_SERIES_PER_QUALITY : versions gardées par qualité et par épisode (défaut 2)
	reload           time.Duration // SFFS_RELOAD : relecture périodique de la base StreamFusion (défaut 24h, 0 = jamais)
	tmdbKey          string        // SFFS_TMDB_KEY : liste des épisodes par saison
	tmdbCache        string        // SFFS_TMDB_CACHE : cache disque des réponses TMDB
	partial          bool          // rechargement ciblé (surveillance) : pas de détail dans le journal
}

func loadConfig() config {
	c := config{
		dsn:         os.Getenv("SF_PG_DSN"),
		base:        strings.TrimRight(os.Getenv("SF_BASE"), "/"),
		playbackCfg: os.Getenv("SF_PLAYBACK_CFG"),
		streamCfg:   os.Getenv("SF_STREAM_CFG"),
		mount:       os.Getenv("SFFS_MOUNT"),
		armTTL:      6 * time.Hour,
	}
	if c.dsn == "" || c.base == "" || c.playbackCfg == "" || c.streamCfg == "" || c.mount == "" {
		log.Fatal("SF_PG_DSN, SF_BASE, SF_PLAYBACK_CFG, SF_STREAM_CFG et SFFS_MOUNT sont obligatoires")
	}
	for _, s := range strings.Split(os.Getenv("SFFS_TMDB"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				log.Fatalf("SFFS_TMDB: %q invalide", s)
			}
			c.tmdb = append(c.tmdb, id)
		}
	}
	re := os.Getenv("SFFS_ALLOW_COMM")
	if re == "" {
		re = "."
	}
	c.allowComm = regexp.MustCompile(re)
	if v := os.Getenv("SFFS_ARM_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Fatalf("SFFS_ARM_TTL: %v", err)
		}
		c.armTTL = d
	}
	c.langs = []string{"fr"}
	if v := strings.TrimSpace(os.Getenv("SFFS_LANGS")); v != "" {
		c.langs = nil
		for _, l := range strings.Split(v, ",") {
			if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
				c.langs = append(c.langs, l)
			}
		}
	}
	c.perQuality = 5
	if v := os.Getenv("SFFS_PER_QUALITY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Fatalf("SFFS_PER_QUALITY: %q invalide", v)
		}
		c.perQuality = n
	}
	c.requireHD = os.Getenv("SFFS_REQUIRE_PREFERRED_HD") == "1"

	c.series = os.Getenv("SFFS_SERIES") == "1"
	c.seriesTmdb = parseIDs("SFFS_SERIES_TMDB")
	c.reload = 24 * time.Hour
	if v := os.Getenv("SFFS_RELOAD"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Fatalf("SFFS_RELOAD: %q invalide", v)
		}
		c.reload = d
	}
	c.seriesPerQuality = 2
	if v := os.Getenv("SFFS_SERIES_PER_QUALITY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Fatalf("SFFS_SERIES_PER_QUALITY: %q invalide", v)
		}
		c.seriesPerQuality = n
	}
	c.tmdbKey = os.Getenv("SFFS_TMDB_KEY")
	c.tmdbCache = os.Getenv("SFFS_TMDB_CACHE")
	if c.series && c.tmdbKey == "" {
		log.Fatal("SFFS_SERIES=1 demande SFFS_TMDB_KEY")
	}
	return c
}

func parseIDs(env string) []int64 {
	var ids []int64
	for _, s := range strings.Split(os.Getenv(env), ",") {
		if s = strings.TrimSpace(s); s != "" {
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				log.Fatalf("%s: %q invalide", env, s)
			}
			ids = append(ids, id)
		}
	}
	return ids
}

// ---------------------------------------------------------------- modèle

// probeInfo est lu par le fork Silo (internal/sfvirtual) à la place de ffprobe.
type probeInfo struct {
	Container   string   `json:"container"`
	Resolution  string   `json:"resolution,omitempty"`
	Width       int      `json:"width,omitempty"`
	Height      int      `json:"height,omitempty"`
	CodecVideo  string   `json:"codec_video,omitempty"`
	CodecAudio  string   `json:"codec_audio,omitempty"`
	Channels    int      `json:"channels,omitempty"`
	HDR         bool     `json:"hdr"`
	DolbyVision bool     `json:"dolby_vision"`
	HDR10Plus   bool     `json:"hdr10_plus"`
	BitDepth    int      `json:"bit_depth,omitempty"`
	Languages   []string `json:"languages,omitempty"`
	Quality     string   `json:"quality,omitempty"`
	Release     string   `json:"release"`
	InfoHash    string   `json:"info_hash"`
	Indexer     string   `json:"indexer,omitempty"`
}

type version struct {
	id        string
	name      string // nom de fichier présenté
	size      uint64
	mtime     time.Time
	infoHash  string
	fileIndex *int
	privacy   string
	probeJSON []byte

	imdb      string // tt… du film ou de la série (vérification du cache)
	mediaType string // "movie" ou "series"
	season    int
	episode   int
	sizeExact bool // false : taille estimée (épisode dans un pack), corrigée à l'armement

	sub       *streamSrc // source en cache servie à la place de celle-ci (hors cache), ou nil
	origSize  uint64
	origExact bool

	// Sélection (comme stremfin) : langue préférée, puis qualité, puis cache et seeders.
	bucket     string // 2160, 1080, 720, 480, unknown
	langScore  int    // rang de la 1re langue préférée trouvée (len(langs) si aucune)
	available  bool   // déjà en cache chez le débrideur selon StreamFusion
	seeders    int
	releaseKey string // release + taille, pour dédoublonner
	year       int
	remux      bool // release REMUX (gardée en priorité dans sa qualité)
}

type movie struct {
	tmdb     int64
	dirName  string
	versions []*version
}

// ---------------------------------------------------------------- base StreamFusion

type parsedData struct {
	ParsedTitle string   `json:"parsed_title"`
	Year        int      `json:"year"`
	Resolution  string   `json:"resolution"`
	Codec       string   `json:"codec"`
	HDR         []string `json:"hdr"`
	Audio       []string `json:"audio"`
	Channels    []string `json:"channels"`
	BitDepth    string   `json:"bit_depth"`
	Languages   []string `json:"languages"`
	Quality     string   `json:"quality"`
}

type torrentFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

var videoExt = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true, ".m2ts": true, ".mov": true, ".wmv": true, ".webm": true, ".mpg": true}

func loadMovies(ctx context.Context, cfg config) ([]*movie, error) {
	conn, err := pgx.Connect(ctx, cfg.dsn)
	if err != nil {
		return nil, fmt.Errorf("connexion base StreamFusion: %w", err)
	}
	defer conn.Close(context.Background())

	q := `select id, raw_title, size, lower(btrim(info_hash)), coalesce(file_name,''), file_index,
	             coalesce(files::text,''), coalesce(privacy,''), coalesce(indexer,''), languages,
	             coalesce(parsed_data::text,''), created_at, tmdb_id, seeders, availability, coalesce(imdb_id,'')
	        from torrent_items
	       where type = 'movie' and tmdb_id is not null and coalesce(btrim(info_hash),'') <> ''`
	args := []any{}
	if len(cfg.tmdb) > 0 {
		q += ` and tmdb_id = any($1)`
		args = append(args, cfg.tmdb)
	}
	q += ` order by tmdb_id, seeders desc, id`
	rows, err := conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := map[int64]*movie{}
	titles := map[int64]map[string]int{}
	imdbs := map[int64]map[string]int{}
	seen := map[string]bool{}
	for rows.Next() {
		var (
			id, raw, hash, fileName, files, privacy, indexer, parsed string
			size, created, tmdb                                      int64
			fileIndex                                                *int32
			langs                                                    []string
			seeders                                                  int32
			available                                                bool
			imdb                                                     string
		)
		if err := rows.Scan(&id, &raw, &size, &hash, &fileName, &fileIndex, &files, &privacy, &indexer, &langs, &parsed, &created, &tmdb, &seeders, &available, &imdb); err != nil {
			return nil, err
		}
		var fidx *int
		if fileIndex != nil {
			v := int(*fileIndex)
			fidx = &v
		}
		key := hash + "|" + fmt.Sprint(fidx)
		if fidx == nil {
			key = hash + "|-"
		}
		if seen[key] {
			continue // même torrent proposé par plusieurs indexeurs
		}
		seen[key] = true

		var pd parsedData
		_ = json.Unmarshal([]byte(parsed), &pd)
		if badQuality.MatchString(strings.ToLower(pd.Quality + " " + raw)) {
			continue // CAM, TS, screener…
		}

		// Fichier réellement lu dans le torrent : taille et nom.
		var tfiles []torrentFile
		_ = json.Unmarshal([]byte(files), &tfiles)
		fsize := size
		if fidx != nil && *fidx >= 0 && *fidx < len(tfiles) {
			fsize, fileName = tfiles[*fidx].SizeBytes, path.Base(tfiles[*fidx].Path)
		} else if len(tfiles) == 1 {
			fsize, fileName = tfiles[0].SizeBytes, path.Base(tfiles[0].Path)
		}
		if fsize <= 0 {
			fsize = size
		}

		m := byID[tmdb]
		if m == nil {
			m = &movie{tmdb: tmdb}
			byID[tmdb] = m
			titles[tmdb] = map[string]int{}
			imdbs[tmdb] = map[string]int{}
		}
		if strings.HasPrefix(imdb, "tt") {
			imdbs[tmdb][imdb]++
		}
		if pd.ParsedTitle != "" {
			titles[tmdb][fmt.Sprintf("%s\x00%d", pd.ParsedTitle, pd.Year)]++
		}
		v := &version{
			id:        id,
			size:      uint64(fsize),
			mtime:     time.Unix(created, 0),
			infoHash:  hash,
			fileIndex: fidx,
			privacy:   privacy,
			mediaType: "movie",
			sizeExact: true,
			bucket:    bucketOf(pd.Resolution, raw),
			langScore: langScore(cfg.langs, raw, langs, pd.Languages),
			available: available,
			seeders:   int(seeders),
			year:      pd.Year,
			remux:     isRemux(raw, pd.Quality),
			releaseKey: strings.ToLower(strings.Join(strings.Fields(strings.Map(func(r rune) rune {
				if r == '.' || r == '_' || r == '-' {
					return ' '
				}
				return r
			}, raw)), " ")) + fmt.Sprintf("|%d", fsize/(100<<20)),
		}
		base := fileName
		if base == "" {
			base = raw
		}
		v.name = versionFileName(base, hash)
		pi := probeFrom(pd, langs, path.Ext(v.name), raw, hash, indexer)
		v.probeJSON, _ = json.Marshal(pi)
		m.versions = append(m.versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*movie, 0, len(byID))
	for tmdb, m := range byID {
		title, year := bestTitle(titles[tmdb])
		if title == "" {
			title = "Film"
		}
		name := cleanName(title)
		if year > 0 {
			name += fmt.Sprintf(" (%d)", year)
		}
		m.dirName = fmt.Sprintf("%s {tmdb-%d}", name, tmdb)
		if best := majority(imdbs[tmdb]); best != "" {
			for _, v := range m.versions {
				v.imdb = best
				if known.isKnown("movie/"+best, v.infoHash) {
					v.available = true // déjà vu en cache : en tête de la sélection
				}
			}
		}
		before := len(m.versions)
		m.versions = selectVersions(m.versions, cfg, year, cfg.perQuality)
		if len(cfg.tmdb) > 0 && !cfg.partial {
			log.Printf("%s : %d versions retenues sur %d", m.dirName, len(m.versions), before)
		}
		if len(m.versions) == 0 {
			continue
		}
		// Noms de fichiers uniques dans le dossier.
		used := map[string]int{}
		for _, v := range m.versions {
			if n := used[v.name]; n > 0 {
				ext := path.Ext(v.name)
				v.name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(v.name, ext), n+1, ext)
			}
			used[v.name]++
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].tmdb < out[j].tmdb })
	return out, nil
}

var remuxRe = regexp.MustCompile(`remux`)

// isRemux : release REMUX (titre ou qualité détectée par StreamFusion).
func isRemux(raw, quality string) bool {
	return remuxRe.MatchString(strings.ToLower(raw + " " + quality))
}

var badQuality = regexp.MustCompile(`\b(cam|camrip|hdcam|ts|hdts|telesync|tc|telecine|scr|screener|dvdscr|r5|3d|sbs|hsbs|fsbs|hou)\b`)

var langPatterns = map[string]*regexp.Regexp{
	"fr": regexp.MustCompile(`(?i)\b(french|truefrench|vff|vfq|vfi|vf2|vf|vof|multi|fr)\b|🇫🇷`),
	"en": regexp.MustCompile(`(?i)\b(english|eng|en|vo)\b|🇬🇧|🇺🇸`),
	"es": regexp.MustCompile(`(?i)\b(spanish|castellano|latino|esp|es)\b`),
	"de": regexp.MustCompile(`(?i)\b(german|deutsch|ger|de)\b`),
	"it": regexp.MustCompile(`(?i)\b(italian|ita|it)\b`),
}

// langScore : rang de la première langue préférée présente (titre de release ou langues
// détectées par StreamFusion), ou len(prefs) si aucune.
func langScore(prefs []string, raw string, lists ...[]string) int {
	text := raw
	for _, l := range lists {
		text += " " + strings.Join(l, " ")
	}
	for i, p := range prefs {
		re := langPatterns[p]
		if re == nil {
			re = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(p) + `\b`)
		}
		if re.MatchString(text) {
			return i
		}
	}
	return len(prefs)
}

func bucketOf(resolution, raw string) string {
	t := strings.ToLower(resolution + " " + raw)
	switch {
	case regexp.MustCompile(`2160p|\b4k\b|\buhd\b`).MatchString(t):
		return "2160"
	case strings.Contains(t, "1080"):
		return "1080"
	case strings.Contains(t, "720p"):
		return "720"
	case regexp.MustCompile(`480p|576p|\bsd\b|dvdrip`).MatchString(t):
		return "480"
	}
	return "unknown"
}

// selectVersions garde, comme stremfin en mode « releases », les meilleures releases de chaque
// qualité : d'abord la meilleure langue disponible (une VF 1080p passe avant une VO 4K), puis
// celles déjà en cache, puis les plus seedées. Un REMUX 4K et un REMUX 1080p sont gardés d'office
// s'il en existe. SD et qualité inconnue seulement à défaut d'autre.
func selectVersions(vs []*version, cfg config, year, perQuality int) []*version {
	if perQuality == 0 {
		return vs
	}
	// Erreurs de rapprochement de la base : année trop éloignée de celle du film.
	kept := vs[:0:0]
	for _, v := range vs {
		if year > 0 && v.year > 0 && (v.year < year-1 || v.year > year+1) {
			continue
		}
		kept = append(kept, v)
	}
	best := len(cfg.langs)
	for _, v := range kept {
		if v.langScore < best {
			best = v.langScore
		}
	}
	if cfg.requireHD && best == len(cfg.langs) {
		return nil // aucune version dans une langue préférée
	}
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if a.langScore != b.langScore {
			return a.langScore < b.langScore
		}
		if a.available != b.available {
			return a.available
		}
		return a.seeders > b.seeders
	})
	seenRelease := map[string]bool{}
	count := map[string]int{}
	var out []*version
	// Au moins un REMUX en 4K et en 1080p quand il en existe un (le mieux classé).
	for _, b := range []string{"2160", "1080"} {
		for _, v := range kept {
			if v.remux && v.bucket == b && v.langScore == best && !seenRelease[v.releaseKey] {
				seenRelease[v.releaseKey] = true
				count[b]++
				out = append(out, v)
				break
			}
		}
	}
	for _, v := range kept {
		// Seulement la meilleure langue trouvée pour ce film (comme le pool de stremfin).
		if v.langScore != best || seenRelease[v.releaseKey] {
			continue
		}
		if v.bucket == "480" || v.bucket == "unknown" {
			continue
		}
		if count[v.bucket] >= perQuality {
			continue
		}
		seenRelease[v.releaseKey] = true
		count[v.bucket]++
		out = append(out, v)
	}
	if len(out) == 0 && !cfg.requireHD { // rien en HD : SD / inconnue
		for _, v := range kept {
			if v.langScore == best && !seenRelease[v.releaseKey] && len(out) < perQuality {
				seenRelease[v.releaseKey] = true
				out = append(out, v)
			}
		}
	}
	order := map[string]int{"2160": 0, "1080": 1, "720": 2, "480": 3, "unknown": 4}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].bucket] < order[out[j].bucket] })
	return out
}

func majority(counts map[string]int) string {
	best, n := "", 0
	for k, c := range counts {
		if c > n || (c == n && k < best) {
			best, n = k, c
		}
	}
	return best
}

func bestTitle(counts map[string]int) (string, int) {
	best, bestN := "", 0
	for k, n := range counts {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	if best == "" {
		return "", 0
	}
	parts := strings.SplitN(best, "\x00", 2)
	y, _ := strconv.Atoi(parts[1])
	return parts[0], y
}

var badChars = regexp.MustCompile(`[\x00-\x1f/\\:*?"<>|]+`)

func cleanName(s string) string {
	s = badChars.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " .")
}

func versionFileName(base, hash string) string {
	base = cleanName(base)
	ext := strings.ToLower(path.Ext(base))
	if videoExt[ext] {
		base = strings.TrimSuffix(base, path.Ext(base))
	} else {
		ext = ".mkv"
	}
	if len(base) > 180 {
		base = strings.TrimSpace(base[:180])
	}
	h := hash
	if len(h) > 8 {
		h = h[:8]
	}
	return fmt.Sprintf("%s [%s]%s", base, h, ext)
}

func probeFrom(pd parsedData, langs []string, ext, raw, hash, indexer string) probeInfo {
	pi := probeInfo{Container: strings.TrimPrefix(strings.ToLower(ext), "."), Release: raw, InfoHash: hash, Indexer: indexer, Quality: pd.Quality}
	if pi.Container == "m4v" {
		pi.Container = "mp4"
	}
	pi.Resolution = strings.ToLower(pd.Resolution)
	switch pi.Resolution {
	case "2160p", "4k":
		pi.Resolution, pi.Width, pi.Height = "2160p", 3840, 2160
	case "1440p":
		pi.Width, pi.Height = 2560, 1440
	case "1080p", "1080i":
		pi.Resolution, pi.Width, pi.Height = "1080p", 1920, 1080
	case "720p":
		pi.Width, pi.Height = 1280, 720
	case "576p":
		pi.Width, pi.Height = 1024, 576
	case "480p":
		pi.Width, pi.Height = 854, 480
	}
	switch c := strings.ToLower(pd.Codec); {
	case strings.Contains(c, "hevc") || strings.Contains(c, "265"):
		pi.CodecVideo = "hevc"
	case strings.Contains(c, "avc") || strings.Contains(c, "264"):
		pi.CodecVideo = "h264"
	case strings.Contains(c, "av1"):
		pi.CodecVideo = "av1"
	case strings.Contains(c, "xvid") || strings.Contains(c, "divx"):
		pi.CodecVideo = "mpeg4"
	case c != "":
		pi.CodecVideo = c
	}
	if len(pd.Audio) > 0 {
		pi.CodecAudio = audioCodec(pd.Audio[0])
	}
	for _, ch := range pd.Channels {
		if n := channelCount(ch); n > pi.Channels {
			pi.Channels = n
		}
	}
	for _, h := range pd.HDR {
		switch strings.ToUpper(h) {
		case "DV", "DOVI", "DOLBY VISION":
			pi.DolbyVision, pi.HDR = true, true
		case "HDR10+":
			pi.HDR10Plus, pi.HDR = true, true
		default:
			pi.HDR = true
		}
	}
	pi.BitDepth, _ = strconv.Atoi(strings.TrimSuffix(pd.BitDepth, "bit"))
	ls := map[string]bool{}
	for _, l := range append(append([]string{}, pd.Languages...), langs...) {
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" && !ls[l] {
			ls[l] = true
			pi.Languages = append(pi.Languages, l)
		}
	}
	return pi
}

func audioCodec(a string) string {
	a = strings.ToLower(a)
	switch {
	case strings.Contains(a, "truehd"), strings.Contains(a, "atmos"):
		return "truehd"
	case strings.Contains(a, "dts"):
		return "dts"
	case strings.Contains(a, "plus"), strings.Contains(a, "ddp"), strings.Contains(a, "e-ac"), strings.Contains(a, "eac3"):
		return "eac3"
	case strings.Contains(a, "dolby digital"), strings.Contains(a, "ac3"), strings.Contains(a, "ac-3"):
		return "ac3"
	case strings.Contains(a, "aac"):
		return "aac"
	case strings.Contains(a, "flac"):
		return "flac"
	case strings.Contains(a, "opus"):
		return "opus"
	case strings.Contains(a, "mp3"):
		return "mp3"
	}
	return ""
}

func channelCount(c string) int {
	switch strings.TrimSpace(c) {
	case "7.1":
		return 8
	case "6.1":
		return 7
	case "5.1":
		return 6
	case "2.0", "stereo":
		return 2
	case "1.0", "mono":
		return 1
	}
	return 0
}

// ---------------------------------------------------------------- résolution des URL de lecture

type resolver struct {
	cfg    config
	cc     *cacheChecker
	client *http.Client
	mu     sync.Mutex
	cache  map[string]resolved
}

type resolved struct {
	url string
	at  time.Time
}

const resolveTTL = 2 * time.Hour

func newResolver(cfg config) *resolver {
	return &resolver{
		cfg:   cfg,
		cc:    &cacheChecker{entries: map[string]*cacheEntry{}, client: &http.Client{Timeout: 90 * time.Second}},
		cache: map[string]resolved{},
		client: &http.Client{
			// On ne suit pas la redirection : on veut l'URL finale du débrideur.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       3 * time.Minute,
		},
	}
}

func (r *resolver) playbackURL(v *version) string {
	var season, episode any
	typ := "movie"
	if v.mediaType == "series" {
		typ, season, episode = "series", fmt.Sprintf("S%02d", v.season), fmt.Sprintf("E%02d", v.episode)
	}
	q := map[string]any{
		"magnet":           "magnet:?xt=urn:btih:" + v.infoHash,
		"type":             typ,
		"file_index":       v.fileIndex,
		"season":           season,
		"episode":          episode,
		"torrent_download": nil,
		"service":          "AD",
		"privacy":          v.privacy,
	}
	b, _ := json.Marshal(q)
	return r.cfg.base + "/playback/" + r.cfg.playbackCfg + "/" + url.PathEscape(base64.StdEncoding.EncodeToString(b))
}

// resolve renvoie l'URL directe du débrideur pour une version (cache 2 h).
func (r *resolver) resolve(ctx context.Context, v *version, fresh bool) (string, error) {
	r.mu.Lock()
	if hit, ok := r.cache[v.id]; ok && !fresh && time.Since(hit.at) < resolveTTL {
		r.mu.Unlock()
		return hit.url, nil
	}
	r.mu.Unlock()

	// Appeler /playback sur un torrent absent du cache le ferait télécharger chez AllDebrid.
	if !r.cached(ctx, v) {
		return "", errNotCached
	}
	target := r.playbackURL(v)
	r.mu.Lock()
	if v.sub != nil {
		target = r.cfg.base + v.sub.path
	}
	r.mu.Unlock()
	t0 := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("streamfusion: %w", err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	var final string
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		loc, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
		if err != nil || loc.String() == "" {
			return "", fmt.Errorf("streamfusion: redirection sans Location")
		}
		final = loc.String()
		if strings.Contains(final, "/static/videos/") {
			// Vidéo d'attente de StreamFusion (« téléchargement en cours ») : pas en cache.
			r.markUncached(v)
			return "", errNotCached
		}
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent:
		// StreamFusion sert lui-même le flux (proxy) : on lit par lui.
		final = target
	default:
		return "", fmt.Errorf("streamfusion: statut %d", resp.StatusCode)
	}
	log.Printf("resolve %s (%s) -> %s en %s", v.id, v.infoHash[:8], hostOf(final), time.Since(t0).Round(time.Millisecond))
	r.mu.Lock()
	r.cache[v.id] = resolved{url: final, at: time.Now()}
	r.mu.Unlock()
	return final, nil
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return "?"
	}
	return p.Host
}

// ---------------------------------------------------------------- FUSE

type state struct {
	cfg   config
	res   *resolver
	data  *http.Client
	mu    sync.Mutex
	armed map[string]time.Time
}

func (s *state) arm(id string) {
	s.mu.Lock()
	s.armed[id] = time.Now().Add(s.cfg.armTTL)
	s.mu.Unlock()
}

func (s *state) isArmed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.armed[id]
	if ok && time.Now().After(exp) {
		delete(s.armed, id)
		return false
	}
	return ok
}

type rootNode struct {
	fs.Inode
	st     *state
	movies []*movie // chargement initial, vidé après le montage
	shows  []*show

	mu            sync.Mutex // un seul rechargement à la fois
	films, series *fs.Inode
	movieDirs     map[int64]string // tmdb -> nom de dossier (gardé d'un rechargement à l'autre)
	showDirs      map[int64]string
}

var _ = (fs.NodeOnAdder)((*rootNode)(nil))

func (n *rootNode) OnAdd(ctx context.Context) {
	n.films = n.NewPersistentInode(ctx, &fs.Inode{}, fs.StableAttr{Mode: syscall.S_IFDIR})
	n.AddChild("films", n.films, true)
	if n.st.cfg.series {
		n.series = n.NewPersistentInode(ctx, &fs.Inode{}, fs.StableAttr{Mode: syscall.S_IFDIR})
		n.AddChild("series", n.series, true)
	}
	n.movieDirs, n.showDirs = map[int64]string{}, map[int64]string{}
	_, _, _ = n.apply(ctx, n.movies, n.shows, false)
	n.movies, n.shows = nil, nil
}

// apply met l'arborescence en conformité avec les listes chargées : ajoute les nouveaux
// fichiers, retire ceux qui ne sont plus retenus et garde tels quels ceux qui n'ont pas changé
// (même nœud, donc même substitution et même taille apprise). Les dossiers existants gardent
// leur nom même si le titre majoritaire a changé dans la base. notify : prévenir le noyau.
// changed : dossiers modifiés ("films/<dossier>", "series/<dossier>"), pour Silo.
func (n *rootNode) apply(ctx context.Context, movies []*movie, shows []*show, notify bool) (added, removed int, changed []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	want := map[string]bool{}
	for _, m := range movies {
		if !n.pinMovie(m, want) {
			continue
		}
		want[m.dirName] = true
		a, r := n.syncFiles(ctx, n.films, m.dirName, m.versions, notify)
		added, removed = added+a, removed+r
		if a+r > 0 {
			changed = append(changed, "films/"+m.dirName)
		}
	}
	r, gone := n.pruneDirs(n.films, want, notify)
	removed += r
	for _, d := range gone {
		changed = append(changed, "films/"+d)
	}
	for id, name := range n.movieDirs {
		if !want[name] {
			delete(n.movieDirs, id)
		}
	}
	if n.series == nil {
		return
	}
	want = map[string]bool{}
	for _, sh := range shows {
		if !n.pinShow(sh, want) {
			continue
		}
		want[sh.dirName] = true
		a, r := n.syncShow(ctx, sh, notify)
		added, removed = added+a, removed+r
		if a+r > 0 {
			changed = append(changed, "series/"+sh.dirName)
		}
	}
	r, gone = n.pruneDirs(n.series, want, notify)
	removed += r
	for _, d := range gone {
		changed = append(changed, "series/"+d)
	}
	for id, name := range n.showDirs {
		if !want[name] {
			delete(n.showDirs, id)
		}
	}
	return
}

// applyPartial applique un rechargement limité aux films mids et aux séries sids : ceux qui
// n'ont plus aucune version retenue disparaissent, le reste de l'arborescence n'est pas touché.
func (n *rootNode) applyPartial(ctx context.Context, movies []*movie, shows []*show, mids, sids []int64) (added, removed int, changed []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	seen := map[int64]bool{}
	for _, m := range movies {
		seen[m.tmdb] = true
		if !n.pinMovie(m, nil) {
			continue
		}
		a, r := n.syncFiles(ctx, n.films, m.dirName, m.versions, true)
		added, removed = added+a, removed+r
		if a+r > 0 {
			changed = append(changed, "films/"+m.dirName)
		}
	}
	for _, id := range mids {
		if name, ok := n.movieDirs[id]; ok && !seen[id] {
			removed += n.removeDir(n.films, name)
			delete(n.movieDirs, id)
			changed = append(changed, "films/"+name)
		}
	}
	if n.series == nil {
		return
	}
	seen = map[int64]bool{}
	for _, sh := range shows {
		seen[sh.tmdb] = true
		if !n.pinShow(sh, nil) {
			continue
		}
		a, r := n.syncShow(ctx, sh, true)
		added, removed = added+a, removed+r
		if a+r > 0 {
			changed = append(changed, "series/"+sh.dirName)
		}
	}
	for _, id := range sids {
		if name, ok := n.showDirs[id]; ok && !seen[id] {
			removed += n.removeDir(n.series, name)
			delete(n.showDirs, id)
			changed = append(changed, "series/"+name)
		}
	}
	return
}

// pinMovie garde le nom de dossier déjà attribué à ce film ; refuse un nouveau film dont le
// nom est déjà pris par un autre (want : noms pris pendant ce passage, nil = arborescence).
func (n *rootNode) pinMovie(m *movie, want map[string]bool) bool {
	if old, ok := n.movieDirs[m.tmdb]; ok {
		m.dirName = old
		return true
	}
	if want != nil && want[m.dirName] || want == nil && n.films.GetChild(m.dirName) != nil {
		return false
	}
	n.movieDirs[m.tmdb] = m.dirName
	return true
}

func (n *rootNode) pinShow(sh *show, want map[string]bool) bool {
	if old, ok := n.showDirs[sh.tmdb]; ok {
		sh.dirName = old
		return true
	}
	if want != nil && want[sh.dirName] || want == nil && n.series.GetChild(sh.dirName) != nil {
		return false
	}
	n.showDirs[sh.tmdb] = sh.dirName
	return true
}

func (n *rootNode) syncShow(ctx context.Context, sh *show, notify bool) (added, removed int) {
	dir, created := n.ensureDir(ctx, n.series, sh.dirName, notify)
	sNotify := notify && !created // dossier tout neuf : le noyau ne le connaît pas encore
	seasons := map[string]bool{}
	for _, sn := range sh.seasonNumbers() {
		name := fmt.Sprintf("Season %02d", sn)
		seasons[name] = true
		a, r := n.syncFiles(ctx, dir, name, sh.seasons[sn], sNotify)
		added, removed = added+a, removed+r
	}
	removed += n.prune(dir, seasons, sNotify)
	return
}

// pruneDirs : comme prune, en renvoyant aussi les noms des dossiers retirés.
func (n *rootNode) pruneDirs(dir *fs.Inode, want map[string]bool, notify bool) (int, []string) {
	var gone []string
	for name := range dir.Children() {
		if !want[name] {
			gone = append(gone, name)
		}
	}
	return n.prune(dir, want, notify), gone
}

func (n *rootNode) removeDir(parent *fs.Inode, name string) int {
	ch := parent.GetChild(name)
	if ch == nil {
		return 0
	}
	files := forgetTree(ch)
	parent.RmChild(name)
	parent.NotifyEntry(name)
	return files
}

func (n *rootNode) ensureDir(ctx context.Context, parent *fs.Inode, name string, notify bool) (*fs.Inode, bool) {
	if d := parent.GetChild(name); d != nil {
		return d, false
	}
	d := parent.NewPersistentInode(ctx, &fs.Inode{}, fs.StableAttr{Mode: syscall.S_IFDIR})
	parent.AddChild(name, d, true)
	if notify {
		parent.NotifyEntry(name)
	}
	return d, true
}

// syncFiles aligne le dossier parent/name sur la liste de versions.
func (n *rootNode) syncFiles(ctx context.Context, parent *fs.Inode, name string, vs []*version, notify bool) (added, removed int) {
	dir, created := n.ensureDir(ctx, parent, name, notify)
	notify = notify && !created
	want := make(map[string]bool, len(vs))
	for _, v := range vs {
		if want[v.name] {
			continue
		}
		want[v.name] = true
		if dir.GetChild(v.name) != nil {
			continue // fichier inchangé : on garde le nœud existant
		}
		f := dir.NewPersistentInode(ctx, &fileNode{st: n.st, v: v}, fs.StableAttr{Mode: syscall.S_IFREG})
		dir.AddChild(v.name, f, true)
		if notify {
			dir.NotifyEntry(v.name)
		}
		added++
	}
	return added, n.prune(dir, want, notify)
}

// prune retire de dir les entrées absentes de want (sous-arborescence comprise).
func (n *rootNode) prune(dir *fs.Inode, want map[string]bool, notify bool) int {
	removed := 0
	for name, ch := range dir.Children() {
		if want[name] {
			continue
		}
		removed += forgetTree(ch)
		dir.RmChild(name)
		if notify {
			dir.NotifyEntry(name)
		}
	}
	return removed
}

// forgetTree libère un nœud et ses descendants ; renvoie le nombre de fichiers retirés.
func forgetTree(in *fs.Inode) int {
	files := 0
	if in.IsDir() {
		for _, ch := range in.Children() {
			files += forgetTree(ch)
		}
	} else {
		files = 1
	}
	in.ForgetPersistent()
	return files
}

// reloadLoop relit toute la base StreamFusion à intervalle régulier (filet de sécurité de la
// surveillance : sources supprimées, rapprochements TMDB tardifs…) sans démonter.
func (n *rootNode) reloadLoop(every time.Duration, silo *siloNotifier) {
	for range time.Tick(every) {
		ctx := context.Background()
		t0 := time.Now()
		movies, shows, err := loadAll(ctx, n.st.cfg)
		if err != nil {
			log.Printf("rechargement : %v (arborescence inchangée)", err)
			continue
		}
		added, removed, changed := n.apply(ctx, movies, shows, true)
		log.Printf("rechargement : %d fichiers ajoutés, %d retirés, %d dossiers modifiés en %s", added, removed, len(changed), time.Since(t0).Round(time.Second))
		silo.add(changed)
		movies, shows = nil, nil
		debug.FreeOSMemory()
	}
}

type fileNode struct {
	fs.Inode
	st *state
	v  *version
}

var (
	_ = (fs.NodeGetattrer)((*fileNode)(nil))
	_ = (fs.NodeOpener)((*fileNode)(nil))
	_ = (fs.NodeGetxattrer)((*fileNode)(nil))
	_ = (fs.NodeListxattrer)((*fileNode)(nil))
)

func (f *fileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFREG | 0o444
	out.Size = f.v.size
	out.Blocks = (f.v.size + 511) / 512
	out.Blksize = 1 << 20
	t := uint64(f.v.mtime.Unix())
	out.Mtime, out.Ctime, out.Atime = t, t, t
	out.SetTimeout(time.Minute)
	return 0
}

var xattrNames = []string{"user.sf.id", "user.sf.probe", "user.sf.arm", "user.sf.armed", "user.sf.cached", "user.sf.served"}

func (f *fileNode) Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	var val []byte
	switch attr {
	case "user.sf.id":
		val = []byte(f.v.id)
	case "user.sf.probe":
		val = f.v.probeJSON
	case "user.sf.cached": // en cache chez le débrideur ? (liste des sources StreamFusion, rien n'est téléchargé)
		val = []byte("0")
		if f.st.res.cached(ctx, f.v) {
			val = []byte("1")
			if f.st.res.substituted(f.v) && !f.v.sizeExact {
				f.learnSize(ctx)
			}
		} else {
			log.Printf("pas en cache : %s %q", f.v.id, f.v.name)
		}
	case "user.sf.served": // hash de la source réellement servie (remplaçante comprise), vide si hors cache
		if !f.st.res.cached(ctx, f.v) {
			return 0, syscall.ENODATA
		}
		if f.st.res.substituted(f.v) && !f.v.sizeExact {
			f.learnSize(ctx)
		}
		val = []byte(f.st.res.servedHash(f.v))
	case "user.sf.armed": // consultation sans effet
		val = []byte("0")
		if f.st.isArmed(f.v.id) {
			val = []byte("1")
		}
	case "user.sf.arm":
		if !f.st.isArmed(f.v.id) {
			log.Printf("arm %s %q (par %s)", f.v.id, f.v.name, callerComm(ctx))
			f.st.arm(f.v.id)
			if !f.v.sizeExact {
				f.learnSize(ctx)
			}
		}
		f.st.arm(f.v.id)
		val = []byte("1")
	default:
		return 0, syscall.ENODATA
	}
	if len(dest) == 0 {
		return uint32(len(val)), 0
	}
	if len(dest) < len(val) {
		return uint32(len(val)), syscall.ERANGE
	}
	return uint32(copy(dest, val)), 0
}

func (f *fileNode) Listxattr(ctx context.Context, dest []byte) (uint32, syscall.Errno) {
	var b []byte
	for _, n := range xattrNames {
		b = append(append(b, n...), 0)
	}
	if len(dest) == 0 {
		return uint32(len(b)), 0
	}
	if len(dest) < len(b) {
		return uint32(len(b)), syscall.ERANGE
	}
	return uint32(copy(dest, b)), 0
}

// learnSize remplace la taille estimée d'un épisode de pack par la taille réelle annoncée par le
// débrideur, et invalide les attributs du noyau avant que la lecture ne commence.
func (f *fileNode) learnSize(ctx context.Context) {
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u, err := f.st.res.resolve(cctx, f.v, false)
	if err != nil {
		log.Printf("taille %s : %v", f.v.id, err)
		return
	}
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := f.st.data.Do(req)
	if err != nil {
		log.Printf("taille %s : %v", f.v.id, err)
		return
	}
	resp.Body.Close()
	cr := resp.Header.Get("Content-Range") // bytes 0-0/<total>
	i := strings.LastIndexByte(cr, '/')
	if resp.StatusCode != http.StatusPartialContent || i < 0 {
		log.Printf("taille %s : statut %d, pas de Content-Range", f.v.id, resp.StatusCode)
		return
	}
	total, err := strconv.ParseUint(cr[i+1:], 10, 64)
	if err != nil || total == 0 {
		return
	}
	if total != f.v.size {
		log.Printf("taille %s : %d estimés -> %d réels", f.v.id, f.v.size, total)
		f.v.size = total
		f.NotifyContent(0, 0)
	}
	f.v.sizeExact = true
}

func callerComm(ctx context.Context) string {
	caller, ok := fuse.FromContext(ctx)
	if !ok {
		return "?"
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", caller.Pid))
	if err != nil {
		return fmt.Sprintf("pid %d", caller.Pid)
	}
	return strings.TrimSpace(string(b))
}

func (f *fileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND) != 0 {
		return nil, 0, syscall.EROFS
	}
	comm := callerComm(ctx)
	if !f.st.isArmed(f.v.id) {
		log.Printf("refus open %s %q par %s : non armé", f.v.id, f.v.name, comm)
		return nil, 0, syscall.EACCES
	}
	if !f.st.cfg.allowComm.MatchString(comm) {
		log.Printf("refus open %s par %s : processus non autorisé", f.v.id, comm)
		return nil, 0, syscall.EACCES
	}
	return &handle{st: f.st, v: f.v}, fuse.FOPEN_DIRECT_IO, 0
}

type handle struct {
	st   *state
	v    *version
	mu   sync.Mutex
	body io.ReadCloser
	pos  int64
}

var (
	_ = (fs.FileReader)((*handle)(nil))
	_ = (fs.FileReleaser)((*handle)(nil))
)

func (h *handle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.body != nil {
		h.body.Close()
		h.body = nil
	}
	return 0
}

// openAt ouvre le flux du débrideur à partir de off. Un lien expiré est renégocié une fois.
func (h *handle) openAt(ctx context.Context, off int64) error {
	if h.body != nil {
		h.body.Close()
		h.body = nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		u, err := h.st.res.resolve(ctx, h.v, attempt > 0)
		if err != nil {
			return err
		}
		// Contexte de fond : le flux survit à la requête FUSE qui l'a ouvert.
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := h.st.data.Do(req)
		if err != nil {
			if attempt == 0 {
				continue
			}
			return err
		}
		if resp.StatusCode == http.StatusPartialContent || (resp.StatusCode == http.StatusOK && off == 0) {
			h.body, h.pos = resp.Body, off
			return nil
		}
		resp.Body.Close()
		log.Printf("flux %s offset %d : statut %d%s", h.v.id, off, resp.StatusCode, map[bool]string{true: ", renégociation", false: ""}[attempt == 0])
	}
	return fmt.Errorf("flux indisponible")
}

func (h *handle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	size := int64(h.v.size)
	if off >= size {
		return fuse.ReadResultData(nil), 0
	}
	want := int64(len(dest))
	if off+want > size {
		want = size - off
	}
	for attempt := 0; attempt < 2; attempt++ {
		if h.body == nil || off != h.pos {
			if err := h.openAt(ctx, off); err != nil {
				log.Printf("lecture %s offset %d : %v", h.v.id, off, err)
				return nil, syscall.EIO
			}
		}
		n, err := io.ReadFull(h.body, dest[:want])
		h.pos += int64(n)
		if err == nil || (n > 0 && (errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF))) {
			return fuse.ReadResultData(dest[:n]), 0
		}
		// Flux coupé : on rouvre à la position courante.
		h.body.Close()
		h.body = nil
		if n > 0 {
			return fuse.ReadResultData(dest[:n]), 0
		}
	}
	return nil, syscall.EIO
}

// ---------------------------------------------------------------- main

// loadAll lit films et séries dans la base StreamFusion et applique la sélection des versions.
func loadAll(ctx context.Context, cfg config) ([]*movie, []*show, error) {
	t0 := time.Now()
	movies, err := loadMovies(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	nv := 0
	for _, m := range movies {
		nv += len(m.versions)
	}
	log.Printf("%d films, %d versions chargés en %s", len(movies), nv, time.Since(t0).Round(time.Millisecond))

	var shows []*show
	if cfg.series {
		t1 := time.Now()
		shows, err = loadSeries(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		ne, nsv := 0, 0
		for _, sh := range shows {
			for _, vs := range sh.seasons {
				nsv += len(vs)
			}
			ne += sh.episodes
		}
		log.Printf("%d séries, %d épisodes, %d versions d'épisodes chargés en %s", len(shows), ne, nsv, time.Since(t1).Round(time.Millisecond))
	}
	initOrig(movies, shows)
	return movies, shows, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cfg := loadConfig()
	ctx := context.Background()

	knownPath := os.Getenv("SFFS_KNOWN_CACHE")
	if knownPath == "" {
		knownPath = os.Getenv("HOME") + "/silo-test/run/known-cached.json"
	}
	loadKnown(knownPath)

	wc := loadWatchConfig()
	wm, err := watermark(ctx, cfg.dsn) // avant le chargement : rien n'est perdu entre les deux
	if err != nil {
		log.Fatal(err)
	}
	movies, shows, err := loadAll(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if os.Getenv("SFFS_DRY_RUN") == "1" {
		if os.Getenv("SFFS_DRY_LIST") == "1" { // liste des versions retenues par film
			for _, m := range movies {
				for _, v := range m.versions {
					log.Printf("%s | %s | remux=%v | %s", m.dirName, v.bucket, v.remux, v.name)
				}
			}
		}
		log.Printf("SFFS_DRY_RUN=1 : pas de montage")
		return
	}

	st := &state{
		cfg:   cfg,
		res:   newResolver(cfg),
		data:  &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 8, ResponseHeaderTimeout: 60 * time.Second}},
		armed: map[string]time.Time{},
	}
	root := &rootNode{st: st, movies: movies, shows: shows}
	silo := newSiloNotifier(wc)
	if cfg.reload > 0 {
		go root.reloadLoop(cfg.reload, silo)
	}
	if wc.every > 0 {
		go root.watchLoop(wc, wm, silo)
	}
	sec := time.Minute
	server, err := fs.Mount(cfg.mount, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName:       "sffs",
			Name:         "sffs",
			SyncRead:     true, // lectures dans l'ordre : un seul flux HTTP par ouverture
			MaxReadAhead: 1 << 20,
			MaxWrite:     1 << 20,
			// SFFS_ALLOW_OTHER=1 : fichiers lisibles par les autres utilisateurs (ex. Silo lancé sous
			// un autre UID dans Docker). Pour un utilisateur non root, demande user_allow_other
			// dans /etc/fuse.conf.
			AllowOther: os.Getenv("SFFS_ALLOW_OTHER") == "1",
		},
		EntryTimeout: &sec,
		AttrTimeout:  &sec,
	})
	if err != nil {
		log.Fatalf("montage %s: %v", cfg.mount, err)
	}
	log.Printf("monté sur %s", cfg.mount)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("démontage")
		server.Unmount()
	}()
	server.Wait()
}
