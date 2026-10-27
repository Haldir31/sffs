package main

// Surveillance de la base StreamFusion : toutes les minutes (SFFS_WATCH), sffs cherche les
// sources ajoutées ou modifiées depuis le dernier passage, recharge seulement les films et
// séries concernés, puis demande à Silo un scan limité aux dossiers qui ont changé.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type watchConfig struct {
	every       time.Duration // SFFS_WATCH (défaut 1m, 0 = pas de surveillance)
	siloURL     string        // SFFS_SILO_URL, ex. http://127.0.0.1:27461
	siloKey     string        // SFFS_SILO_KEY : clé d'API admin de Silo
	siloKeyFile string        // SFFS_SILO_KEY_FILE : fichier de la clé, relu à chaque passage (clé créée après le démarrage)
	siloFilms   string        // SFFS_SILO_FILMS : chemin du dossier films vu par Silo (défaut /mnt/sf/films)
	siloSeries  string        // SFFS_SILO_SERIES (défaut /mnt/sf/series)
}

func loadWatchConfig() watchConfig {
	w := watchConfig{every: time.Minute, siloURL: os.Getenv("SFFS_SILO_URL"), siloKey: os.Getenv("SFFS_SILO_KEY"), siloKeyFile: os.Getenv("SFFS_SILO_KEY_FILE"),
		siloFilms: "/mnt/sf/films", siloSeries: "/mnt/sf/series"}
	if v := os.Getenv("SFFS_WATCH"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Fatalf("SFFS_WATCH: %q invalide", v)
		}
		w.every = d
	}
	if v := os.Getenv("SFFS_SILO_FILMS"); v != "" {
		w.siloFilms = v
	}
	if v := os.Getenv("SFFS_SILO_SERIES"); v != "" {
		w.siloSeries = v
	}
	return w
}

// La surveillance repère les lignes nouvelles ou modifiées par leur numéro de transaction Postgres
// (xmin), pas par updated_at : les torrents reçus par la synchronisation de StreamFusion gardent la
// date de leur source, plus ancienne, et ne seraient jamais vus. xmin change à chaque écriture.
//
// Le repère wm est un numéro de transaction sur 64 bits (pg_snapshot_xmin : toute transaction
// antérieure est terminée). xmin n'a que 32 bits : la requête en tient compte s'il a rebouclé.

// watermark renvoie le repère de départ de la surveillance.
func watermark(ctx context.Context, dsn string) (int64, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return 0, err
	}
	defer conn.Close(context.Background())
	var wm int64
	err = conn.QueryRow(ctx, `select pg_snapshot_xmin(pg_current_snapshot())::text::bigint`).Scan(&wm)
	return wm, err
}

// Empreinte des sources d'un titre dans StreamFusion : nombre de sources et XOR des hachages de leurs
// info_hash. Les tâches de fond de StreamFusion réécrivent des dizaines de milliers de lignes (xmin
// change) sans en changer les sources : seul un titre dont l'empreinte change est rechargé.
type titleKey struct {
	series bool
	id     int64
}

type titleFP struct{ n, x int64 }

// Lignes que sffs charge (mêmes filtres que loadMovies et loadSeries), classées film ou série.
const fpFilter = `tmdb_id is not null and coalesce(btrim(info_hash),'') <> ''
	and (type = 'movie' or (type in ('series','show') and coalesce(tmdb_media_type,'') <> 'movie'))`

// fpWithAvailability : en mode « en cache seulement », l'empreinte tient compte de l'état du cache
// (availability) : une version qui passe en cache, ou n'y est plus, recharge son titre.
var fpWithAvailability bool

// fingerprints calcule l'empreinte de tous les titres (ids vide) ou des titres ids.
func fingerprints(ctx context.Context, conn *pgx.Conn, ids []int64) (map[titleKey]titleFP, error) {
	src := `lower(btrim(info_hash))`
	if fpWithAvailability {
		src = `lower(btrim(info_hash)) || availability::text`
	}
	q := `select tmdb_id, type <> 'movie', count(*), bit_xor(hashtextextended(` + src + `, 0))
	        from torrent_items where ` + fpFilter
	args := []any{}
	if len(ids) > 0 {
		q += ` and tmdb_id = any($1)`
		args = append(args, ids)
	}
	rows, err := conn.Query(ctx, q+` group by 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fps := map[titleKey]titleFP{}
	for rows.Next() {
		var (
			id     int32
			series bool
			fp     titleFP
		)
		if err := rows.Scan(&id, &series, &fp.n, &fp.x); err != nil {
			return nil, err
		}
		fps[titleKey{series, int64(id)}] = fp
	}
	return fps, rows.Err()
}

// allFingerprints : empreintes de départ (au démarrage, après watermark).
func allFingerprints(ctx context.Context, dsn string) (map[titleKey]titleFP, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	return fingerprints(ctx, conn, nil)
}

// Au-delà, recalculer toutes les empreintes (une lecture de la table, ~1 s) coûte moins que des
// milliers de recherches par tmdb_id.
const maxTargetedTitles = 200

// changedSince liste les films et séries dont les sources ont changé depuis le repère wm (fps est
// mis à jour), et renvoie le repère suivant.
func changedSince(ctx context.Context, dsn string, wm int64, fps map[titleKey]titleFP) (movies, shows []int64, newWM int64, err error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, nil, wm, err
	}
	defer conn.Close(context.Background())
	// Repère suivant pris avant la lecture : une transaction encore en cours a un numéro au moins
	// égal, elle sera vue au passage suivant. Une ligne vue deux fois ne coûte qu'une comparaison.
	var xmax int64
	if err := conn.QueryRow(ctx, `select pg_snapshot_xmin(s)::text::bigint, pg_snapshot_xmax(s)::text::bigint
	                                from pg_current_snapshot() s`).Scan(&newWM, &xmax); err != nil {
		return nil, nil, wm, err
	}
	lo, hi := wm&0xffffffff, xmax&0xffffffff
	wrapped := wm>>32 != xmax>>32 // les numéros sur 32 bits ont rebouclé depuis wm
	rows, err := conn.Query(ctx, `select distinct tmdb_id, type <> 'movie'
	                                from torrent_items
	                               where (xmin::text::bigint >= $1 or ($3 and xmin::text::bigint < $2))
	                                 and `+fpFilter, lo, hi, wrapped)
	if err != nil {
		return nil, nil, wm, err
	}
	touched := map[titleKey]bool{}
	ids := map[int64]bool{}
	for rows.Next() {
		var (
			id     int32
			series bool
		)
		if err := rows.Scan(&id, &series); err != nil {
			rows.Close()
			return nil, nil, wm, err
		}
		touched[titleKey{series, int64(id)}] = true
		ids[int64(id)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, wm, err
	}
	if len(touched) == 0 {
		return nil, nil, newWM, nil
	}

	var fresh map[titleKey]titleFP
	full := len(ids) > maxTargetedTitles
	if full {
		fresh, err = fingerprints(ctx, conn, nil)
	} else {
		list := make([]int64, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		fresh, err = fingerprints(ctx, conn, list)
	}
	if err != nil {
		return nil, nil, wm, err
	}
	changed := map[titleKey]bool{}
	check := func(k titleKey) {
		f, ok := fresh[k]
		if old, had := fps[k]; ok != had || f != old {
			changed[k] = true
		}
		if ok {
			fps[k] = f
		} else {
			delete(fps, k)
		}
	}
	if full {
		for k := range fresh {
			check(k)
		}
		for k := range fps {
			if _, ok := fresh[k]; !ok {
				check(k)
			}
		}
	} else {
		for k := range touched {
			check(k)
		}
	}
	if len(touched) >= 50 || len(changed) > 0 {
		mode := "ciblé"
		if full {
			mode = "complet"
		}
		log.Printf("surveillance : %d titres réécrits dans StreamFusion, %d avec des sources modifiées (contrôle %s)",
			len(touched), len(changed), mode)
	}
	for k := range changed {
		if k.series {
			shows = append(shows, k.id)
		} else {
			movies = append(movies, k.id)
		}
	}
	sort.Slice(movies, func(i, j int) bool { return movies[i] < movies[j] })
	sort.Slice(shows, func(i, j int) bool { return shows[i] < shows[j] })
	return movies, shows, newWM, nil
}

// watchLoop : surveillance minute par minute, rechargement ciblé et scans Silo ciblés.
func (n *rootNode) watchLoop(wc watchConfig, wm int64, fps map[titleKey]titleFP, silo *siloNotifier) {
	cfg := n.st.cfg
	for range time.Tick(wc.every) {
		ctx := context.Background()
		mids, sids, newWM, err := changedSince(ctx, cfg.dsn, wm, fps)
		if err != nil {
			log.Printf("surveillance : %v", err)
			continue
		}
		wm = newWM
		if !cfg.series {
			sids = nil
		}
		if len(cfg.tmdb) > 0 { // instance limitée à certains films
			mids = intersect(mids, cfg.tmdb)
		}
		if len(cfg.seriesTmdb) > 0 {
			sids = intersect(sids, cfg.seriesTmdb)
		}
		if len(mids)+len(sids) > 0 {
			t0 := time.Now()
			pc := cfg
			pc.partial = true
			var movies []*movie
			var shows []*show
			if len(mids) > 0 {
				pc.tmdb = mids
				movies, err = loadMovies(ctx, pc)
			}
			if err == nil && len(sids) > 0 {
				pc.seriesTmdb = sids
				shows, err = loadSeries(ctx, pc)
			}
			if err != nil {
				log.Printf("surveillance : chargement : %v", err)
				continue
			}
			initOrig(movies, shows)
			added, removed, changed := n.applyPartial(ctx, movies, shows, mids, sids)
			if added+removed > 0 {
				log.Printf("surveillance : %d films et %d séries modifiés dans StreamFusion -> %d fichiers ajoutés, %d retirés, %d dossiers (%s)",
					len(mids), len(sids), added, removed, len(changed), time.Since(t0).Round(time.Millisecond))
			}
			silo.add(changed)
		}
		silo.flush(ctx)
	}
}

func intersect(a, b []int64) []int64 {
	in := map[int64]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []int64
	for _, x := range a {
		if in[x] {
			out = append(out, x)
		}
	}
	return out
}

func initOrig(movies []*movie, shows []*show) {
	for _, m := range movies {
		for _, v := range m.versions {
			v.origSize, v.origExact = v.size, v.sizeExact
		}
	}
	for _, sh := range shows {
		for _, vs := range sh.seasons {
			for _, v := range vs {
				v.origSize, v.origExact = v.size, v.sizeExact
			}
		}
	}
}

// ---------------------------------------------------------------- Silo

// siloNotifier demande à Silo un scan limité à chaque dossier modifié. Les demandes refusées
// (Silo arrêté, scan déjà en cours…) sont gardées et renvoyées au passage suivant.
type siloNotifier struct {
	wc      watchConfig
	http    *http.Client
	mu      sync.Mutex
	pending map[string]bool // "films/<dossier>" ou "series/<dossier>"
}

func newSiloNotifier(wc watchConfig) *siloNotifier {
	return &siloNotifier{wc: wc, http: &http.Client{Timeout: 30 * time.Second}, pending: map[string]bool{}}
}

func (s *siloNotifier) enabled() bool { return s != nil && s.wc.siloURL != "" && s.key() != "" }

// key renvoie la clé d'API Silo : SFFS_SILO_KEY, sinon le contenu de SFFS_SILO_KEY_FILE.
func (s *siloNotifier) key() string {
	if s.wc.siloKey != "" || s.wc.siloKeyFile == "" {
		return s.wc.siloKey
	}
	b, err := os.ReadFile(s.wc.siloKeyFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s *siloNotifier) add(dirs []string) {
	if !s.enabled() {
		return
	}
	s.mu.Lock()
	for _, d := range dirs {
		s.pending[d] = true
	}
	s.mu.Unlock()
}

// errRateLimited : Silo limite le nombre de requêtes ; on reprend au passage suivant.
var errRateLimited = errors.New("limite de requêtes Silo atteinte")

// Au-delà de ce nombre de dossiers, un scan de la bibliothèque entière est plus simple.
const maxSubtreeScans = 200

func (s *siloNotifier) flush(ctx context.Context) {
	if !s.enabled() {
		return
	}
	s.mu.Lock()
	dirs := make([]string, 0, len(s.pending))
	for d := range s.pending {
		dirs = append(dirs, d)
	}
	s.mu.Unlock()
	if len(dirs) == 0 {
		return
	}
	sort.Strings(dirs)
	var paths []string
	if len(dirs) > maxSubtreeScans {
		films, series := false, false
		for _, d := range dirs {
			if len(d) > 6 && d[:6] == "films/" {
				films = true
			} else {
				series = true
			}
		}
		if films {
			paths = append(paths, s.wc.siloFilms)
		}
		if series {
			paths = append(paths, s.wc.siloSeries)
		}
	}
	ok, failed := 0, 0
	var lastErr error
	if paths != nil { // scan de bibliothèque : tout ou rien
		for _, p := range paths {
			if err := s.scan(ctx, p); err != nil {
				failed++
				lastErr = err
			}
		}
		if failed == 0 {
			s.mu.Lock()
			for _, d := range dirs {
				delete(s.pending, d)
			}
			s.mu.Unlock()
			log.Printf("silo : scan des bibliothèques demandé (%d dossiers modifiés)", len(dirs))
		}
	} else {
		for _, d := range dirs {
			if err := s.scan(ctx, s.siloPath(d)); err != nil {
				if errors.Is(err, errRateLimited) {
					log.Printf("silo : %d scans ciblés demandés, limite de requêtes atteinte, %d restants pour le prochain passage", ok, len(dirs)-ok-failed)
					ok = 0 // déjà journalisé
					break
				}
				failed++
				lastErr = err
				continue
			}
			ok++
			s.mu.Lock()
			delete(s.pending, d)
			s.mu.Unlock()
		}
		if ok > 0 {
			log.Printf("silo : %d scans ciblés demandés", ok)
		}
	}
	if failed > 0 {
		log.Printf("silo : %d demandes de scan en échec, nouvel essai au prochain passage (%v)", failed, lastErr)
	}
}

func (s *siloNotifier) siloPath(d string) string {
	if len(d) > 6 && d[:6] == "films/" {
		return s.wc.siloFilms + "/" + d[6:]
	}
	return s.wc.siloSeries + "/" + d[len("series/"):]
}

func (s *siloNotifier) scan(ctx context.Context, p string) error {
	body, _ := json.Marshal(map[string]string{"path": p})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.wc.siloURL+"/api/v2/scan", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.key())
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		// Dossier inconnu de Silo (déjà disparu…) : inutile de réessayer, le scan nocturne s'en charge.
		log.Printf("silo : scan de %s refusé (statut %d), abandonné", p, resp.StatusCode)
		return nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return errRateLimited
	}
	if resp.StatusCode/100 != 2 {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		msg := b.String()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("%s : statut %d %s", p, resp.StatusCode, msg)
	}
	return nil
}
