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

// watermark renvoie le updated_at le plus récent de la base (point de départ de la surveillance).
func watermark(ctx context.Context, dsn string) (int64, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return 0, err
	}
	defer conn.Close(context.Background())
	var wm int64
	err = conn.QueryRow(ctx, `select coalesce(max(updated_at),0) from torrent_items`).Scan(&wm)
	return wm, err
}

// changedSince liste les films et séries dont une source a été ajoutée ou modifiée après wm.
func changedSince(ctx context.Context, dsn string, wm int64) (movies, shows []int64, newWM int64, err error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, nil, wm, err
	}
	defer conn.Close(context.Background())
	// Petit recouvrement (5 s) : une ligne écrite pendant la requête précédente n'est pas perdue.
	rows, err := conn.Query(ctx, `select tmdb_id, type, coalesce(tmdb_media_type,''), updated_at
	                                from torrent_items
	                               where updated_at > $1 and tmdb_id is not null and coalesce(btrim(info_hash),'') <> ''`, wm-5)
	if err != nil {
		return nil, nil, wm, err
	}
	defer rows.Close()
	newWM = wm
	mset, sset := map[int64]bool{}, map[int64]bool{}
	for rows.Next() {
		var (
			id        int32
			typ, mt   string
			updatedAt int64
		)
		if err := rows.Scan(&id, &typ, &mt, &updatedAt); err != nil {
			return nil, nil, wm, err
		}
		if updatedAt > newWM {
			newWM = updatedAt
		}
		switch {
		case typ == "movie":
			mset[int64(id)] = true
		case (typ == "series" || typ == "show") && mt != "movie":
			sset[int64(id)] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, wm, err
	}
	for id := range mset {
		movies = append(movies, id)
	}
	for id := range sset {
		shows = append(shows, id)
	}
	sort.Slice(movies, func(i, j int) bool { return movies[i] < movies[j] })
	sort.Slice(shows, func(i, j int) bool { return shows[i] < shows[j] })
	return movies, shows, newWM, nil
}

// watchLoop : surveillance minute par minute, rechargement ciblé et scans Silo ciblés.
func (n *rootNode) watchLoop(wc watchConfig, wm int64, silo *siloNotifier) {
	cfg := n.st.cfg
	for range time.Tick(wc.every) {
		ctx := context.Background()
		mids, sids, newWM, err := changedSince(ctx, cfg.dsn, wm)
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
