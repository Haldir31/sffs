package main

// Séries : chaque torrent (épisode seul, pack de saison ou intégrale) devient une version de
// chacun des épisodes qu'il contient :
//
//	series/<Titre> (<Année>) {tmdb-<id>}/Season 01/<Titre> - S01E01 - <release> [<hash8>].mkv
//
// La liste des épisodes de chaque saison vient de TMDB (un appel par série, mis en cache disque),
// car la base StreamFusion connaît rarement le contenu des packs. À la lecture, StreamFusion
// choisit lui-même le fichier de l'épisode dans le pack (season/episode dans l'URL /playback).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type show struct {
	tmdb     int64
	dirName  string
	seasons  map[int][]*version // saison -> versions de tous ses épisodes
	episodes int                // épisodes ayant au moins une version
}

func (s *show) seasonNumbers() []int {
	ns := make([]int, 0, len(s.seasons))
	for n := range s.seasons {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	return ns
}

type seriesParsed struct {
	parsedData
	Seasons  []int `json:"seasons"`
	Episodes []int `json:"episodes"`
	Complete bool  `json:"complete"`
}

// épisode couvert par un torrent
type coverage struct {
	season, episode int
	fileIndex       *int
	size            int64
	exact           bool
	fileName        string
}

var episodeInPath = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[ ._-]?e(\d{1,3})(?:[^0-9]|$)|(?:^|[^0-9])(\d{1,2})x(\d{2,3})(?:[^0-9]|$)`)

func parseEpisode(name string) (int, int, bool) {
	m := episodeInPath.FindStringSubmatch(name)
	if m == nil {
		return 0, 0, false
	}
	a, b := m[1], m[2]
	if a == "" {
		a, b = m[3], m[4]
	}
	s, _ := strconv.Atoi(a)
	e, _ := strconv.Atoi(b)
	return s, e, e > 0
}

// ---------------------------------------------------------------- TMDB

type tvInfo struct {
	Name    string      `json:"name"`
	Year    int         `json:"year"`
	Seasons map[int]int `json:"seasons"` // saison -> nombre d'épisodes
	Fetched int64       `json:"fetched"`
	Missing bool        `json:"missing"`
}

type tmdbClient struct {
	key   string
	path  string
	http  *http.Client
	mu    sync.Mutex
	cache map[int64]*tvInfo
	tick  *time.Ticker
}

const tmdbTTL = 7 * 24 * time.Hour

func newTMDB(cfg config) *tmdbClient {
	c := &tmdbClient{key: cfg.tmdbKey, path: cfg.tmdbCache, http: &http.Client{Timeout: 20 * time.Second}, cache: map[int64]*tvInfo{}, tick: time.NewTicker(30 * time.Millisecond)}
	if c.path != "" {
		if b, err := os.ReadFile(c.path); err == nil {
			_ = json.Unmarshal(b, &c.cache)
		}
	}
	return c
}

func (c *tmdbClient) save() {
	if c.path == "" {
		return
	}
	c.mu.Lock()
	b, err := json.Marshal(c.cache)
	c.mu.Unlock()
	if err == nil {
		tmp := c.path + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, c.path)
		}
	}
}

func (c *tmdbClient) get(ctx context.Context, id int64) *tvInfo {
	c.mu.Lock()
	hit := c.cache[id]
	c.mu.Unlock()
	if hit != nil && time.Since(time.Unix(hit.Fetched, 0)) < tmdbTTL {
		return hit
	}
	<-c.tick.C // ~30 requêtes/s au plus
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.themoviedb.org/3/tv/%d?api_key=%s&language=fr-FR", id, c.key), nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return hit // ancienne réponse si on en a une
	}
	defer resp.Body.Close()
	info := &tvInfo{Fetched: time.Now().Unix(), Seasons: map[int]int{}}
	switch resp.StatusCode {
	case http.StatusOK:
		var d struct {
			Name         string `json:"name"`
			FirstAirDate string `json:"first_air_date"`
			Seasons      []struct {
				SeasonNumber int `json:"season_number"`
				EpisodeCount int `json:"episode_count"`
			} `json:"seasons"`
		}
		if json.NewDecoder(resp.Body).Decode(&d) != nil {
			return hit
		}
		info.Name = d.Name
		if len(d.FirstAirDate) >= 4 {
			info.Year, _ = strconv.Atoi(d.FirstAirDate[:4])
		}
		for _, s := range d.Seasons {
			if s.EpisodeCount > 0 {
				info.Seasons[s.SeasonNumber] = s.EpisodeCount
			}
		}
	case http.StatusNotFound:
		info.Missing = true
	default:
		return hit
	}
	c.mu.Lock()
	c.cache[id] = info
	c.mu.Unlock()
	return info
}

// ---------------------------------------------------------------- chargement

type seriesRow struct {
	id, raw, hash, fileName, files, privacy, indexer, parsed string
	size, created, tmdb                                      int64
	fileIndex                                                *int32
	langs                                                    []string
	seeders                                                  int32
	available                                                bool
	imdb                                                     string
}

func loadSeries(ctx context.Context, cfg config) ([]*show, error) {
	conn, err := pgx.Connect(ctx, cfg.dsn)
	if err != nil {
		return nil, fmt.Errorf("connexion base StreamFusion: %w", err)
	}
	defer conn.Close(context.Background())

	q := `select id, raw_title, size, lower(btrim(info_hash)), coalesce(file_name,''), file_index,
	             coalesce(files::text,''), coalesce(privacy,''), coalesce(indexer,''), languages,
	             coalesce(parsed_data::text,''), created_at, tmdb_id, seeders, availability, coalesce(imdb_id,'')
	        from torrent_items
	       where type in ('series','show') and tmdb_id is not null
	         and coalesce(tmdb_media_type,'tv') <> 'movie'
	         and coalesce(btrim(info_hash),'') <> ''`
	args := []any{}
	if len(cfg.seriesTmdb) > 0 {
		q += ` and tmdb_id = any($1)`
		args = append(args, cfg.seriesTmdb)
	}
	q += ` order by tmdb_id, seeders desc, id`
	rows, err := conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	byShow := map[int64][]seriesRow{}
	for rows.Next() {
		var r seriesRow
		if err := rows.Scan(&r.id, &r.raw, &r.size, &r.hash, &r.fileName, &r.fileIndex, &r.files, &r.privacy, &r.indexer, &r.langs, &r.parsed, &r.created, &r.tmdb, &r.seeders, &r.available, &r.imdb); err != nil {
			rows.Close()
			return nil, err
		}
		byShow[r.tmdb] = append(byShow[r.tmdb], r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	tm := newTMDB(cfg)
	ids := make([]int64, 0, len(byShow))
	for id := range byShow {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	// Appels TMDB en parallèle (4), sauvegarde du cache régulière.
	infos := make(map[int64]*tvInfo, len(ids))
	var mu sync.Mutex
	work := make(chan int64)
	var wg sync.WaitGroup
	done := 0
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				info := tm.get(ctx, id)
				mu.Lock()
				infos[id] = info
				done++
				if done%2000 == 0 {
					log.Printf("TMDB : %d/%d séries", done, len(ids))
					go tm.save()
				}
				mu.Unlock()
			}
		}()
	}
	for _, id := range ids {
		work <- id
	}
	close(work)
	wg.Wait()
	tm.save()

	var out []*show
	for _, id := range ids {
		info := infos[id]
		if info == nil || info.Missing || info.Name == "" {
			continue
		}
		sh := buildShow(cfg, id, info, byShow[id])
		if sh != nil {
			out = append(out, sh)
		}
	}
	return out, nil
}

func buildShow(cfg config, tmdb int64, info *tvInfo, rows []seriesRow) *show {
	name := cleanName(info.Name)
	if info.Year > 0 {
		name += fmt.Sprintf(" (%d)", info.Year)
	}
	sh := &show{tmdb: tmdb, dirName: fmt.Sprintf("%s {tmdb-%d}", name, tmdb), seasons: map[int][]*version{}}

	imdbCount := map[string]int{}
	for _, r := range rows {
		if strings.HasPrefix(r.imdb, "tt") {
			imdbCount[r.imdb]++
		}
	}
	imdb := majority(imdbCount)
	type epKey struct{ s, e int }
	cands := map[epKey][]*version{}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.hash] {
			continue // même torrent proposé par plusieurs indexeurs
		}
		seen[r.hash] = true
		var pd seriesParsed
		_ = json.Unmarshal([]byte(r.parsed), &pd)
		if unplayable(r.raw, pd.Quality) || excludedByChoice(cfg, r.raw, pd.Quality) {
			continue
		}
		covs := coverOf(r, pd, info)
		if len(covs) == 0 {
			continue
		}
		pi := probeFrom(pd.parsedData, r.langs, ".mkv", r.raw, r.hash, r.indexer)
		probe, _ := json.Marshal(pi)
		bucket := bucketOf(pd.Resolution, r.raw)
		lscore := langScore(cfg.langs, r.raw, r.langs, pd.Languages)
		relKey := strings.ToLower(strings.Join(strings.Fields(strings.Map(func(c rune) rune {
			if c == '.' || c == '_' || c == '-' {
				return ' '
			}
			return c
		}, r.raw)), " "))
		for _, c := range covs {
			base := c.fileName
			if base == "" {
				base = r.raw
			}
			ext := strings.ToLower(path.Ext(base))
			pj := probe
			if videoExt[ext] && ext != ".mkv" {
				pi2 := pi
				pi2.Container = strings.TrimPrefix(ext, ".")
				pj, _ = json.Marshal(pi2)
			}
			v := &version{
				id:         fmt.Sprintf("%s-s%02de%02d", r.id, c.season, c.episode),
				size:       uint64(c.size),
				mtime:      time.Unix(r.created, 0),
				infoHash:   r.hash,
				fileIndex:  c.fileIndex,
				privacy:    r.privacy,
				probeJSON:  pj,
				imdb:       imdb,
				mediaType:  "series",
				season:     c.season,
				episode:    c.episode,
				sizeExact:  c.exact,
				bucket:     bucket,
				langScore:  lscore,
				available:  r.available,
				seeders:    int(r.seeders),
				releaseKey: relKey,
				remux:      isRemux(r.raw, pd.Quality),
			}
			v.name = fmt.Sprintf("%s - S%02dE%02d - %s", cleanName(info.Name), c.season, c.episode, versionFileName(base, r.hash))
			k := epKey{c.season, c.episode}
			cands[k] = append(cands[k], v)
		}
	}
	for k, vs := range cands {
		if imdb != "" {
			key := fmt.Sprintf("series/%s:%d:%d", imdb, k.s, k.e)
			for _, v := range vs {
				if known.isKnown(key, v.infoHash) {
					v.available = true
				}
			}
		}
		vs = selectVersions(vs, cfg, 0, cfg.seriesPerQuality)
		if len(vs) == 0 {
			continue
		}
		sh.episodes++
		sh.seasons[k.s] = append(sh.seasons[k.s], vs...)
	}
	if sh.episodes == 0 {
		return nil
	}
	for _, vs := range sh.seasons {
		sort.Slice(vs, func(i, j int) bool {
			if vs[i].episode != vs[j].episode {
				return vs[i].episode < vs[j].episode
			}
			return vs[i].name < vs[j].name
		})
		used := map[string]int{}
		for _, v := range vs {
			if n := used[v.name]; n > 0 {
				ext := path.Ext(v.name)
				v.name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(v.name, ext), n+1, ext)
			}
			used[v.name]++
		}
	}
	return sh
}

// coverOf liste les épisodes qu'un torrent contient.
func coverOf(r seriesRow, pd seriesParsed, info *tvInfo) []coverage {
	// 1. Liste de fichiers connue : épisodes exacts, avec index et taille.
	var tfiles []torrentFile
	_ = json.Unmarshal([]byte(r.files), &tfiles)
	var out []coverage
	for i, f := range tfiles {
		if !videoExt[strings.ToLower(path.Ext(f.Path))] || f.SizeBytes < 50<<20 {
			continue
		}
		if s, e, ok := parseEpisode(path.Base(f.Path)); ok {
			idx := i
			out = append(out, coverage{season: s, episode: e, fileIndex: &idx, size: f.SizeBytes, exact: true, fileName: path.Base(f.Path)})
		}
	}
	if len(out) > 0 {
		return out
	}
	// 2. Épisode(s) précis dans le titre.
	if len(pd.Seasons) == 1 && len(pd.Episodes) > 0 {
		each := r.size / int64(len(pd.Episodes))
		for _, e := range pd.Episodes {
			out = append(out, coverage{season: pd.Seasons[0], episode: e, size: each, exact: len(pd.Episodes) == 1 && len(tfiles) <= 1})
		}
		return out
	}
	// 3. Pack de saison(s), ou intégrale : épisodes selon TMDB, taille estimée.
	var seasons []int
	switch {
	case len(pd.Seasons) > 0:
		seasons = pd.Seasons
	case pd.Complete:
		for s := range info.Seasons {
			if s > 0 {
				seasons = append(seasons, s)
			}
		}
	}
	total := 0
	for _, s := range seasons {
		total += info.Seasons[s]
	}
	if total == 0 {
		return nil
	}
	each := r.size / int64(total)
	for _, s := range seasons {
		for e := 1; e <= info.Seasons[s]; e++ {
			out = append(out, coverage{season: s, episode: e, size: each})
		}
	}
	return out
}
