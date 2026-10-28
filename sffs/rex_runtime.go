package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// tmdbRuntimes : durées TMDB (en minutes) des films et des épisodes, gardées sur disque. Elles
// donnent une durée aux fichiers que personne n'a encore lus (sans elle, pas de barre de
// progression dans les applications Plex). Une seule requête par film et par saison ; une durée
// inconnue de TMDB est gardée à 0 et n'est pas redemandée.
type tmdbRuntimes struct {
	key, path string
	http      *http.Client
	tick      *time.Ticker

	mu    sync.Mutex
	m     map[string]int // "m<film>", "e<série>/<saison>/<épisode>", "s<série>/<saison>" (saison lue)
	dirty bool
}

func newTMDBRuntimes(key, path string) *tmdbRuntimes {
	r := &tmdbRuntimes{key: key, path: path, http: &http.Client{Timeout: 20 * time.Second},
		tick: time.NewTicker(30 * time.Millisecond), m: map[string]int{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &r.m)
	}
	return r
}

func (r *tmdbRuntimes) lookup(k string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.m[k]
	return v, ok
}

func (r *tmdbRuntimes) set(vals map[string]int) {
	r.mu.Lock()
	for k, v := range vals {
		r.m[k] = v
	}
	r.dirty = true
	r.mu.Unlock()
}

// fetch interroge TMDB ; ok=false : erreur passagère (rien n'est gardé, nouvel essai plus tard).
func (r *tmdbRuntimes) fetch(path string, out any) (found, ok bool) {
	<-r.tick.C // ~30 requêtes/s au plus
	resp, err := r.http.Get(fmt.Sprintf("https://api.themoviedb.org/3/%s?api_key=%s", path, r.key))
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(out) == nil, true
	case http.StatusNotFound:
		return false, true
	}
	return false, false
}

func (r *tmdbRuntimes) movie(id int64) int {
	if r == nil {
		return 0
	}
	k := fmt.Sprintf("m%d", id)
	if v, ok := r.lookup(k); ok {
		return v
	}
	var d struct {
		Runtime int `json:"runtime"`
	}
	if _, ok := r.fetch(fmt.Sprintf("movie/%d", id), &d); !ok {
		return 0
	}
	r.set(map[string]int{k: d.Runtime})
	return d.Runtime
}

func (r *tmdbRuntimes) episode(show int64, season, episode int) int {
	if r == nil {
		return 0
	}
	k := fmt.Sprintf("e%d/%d/%d", show, season, episode)
	if v, ok := r.lookup(k); ok {
		return v
	}
	sk := fmt.Sprintf("s%d/%d", show, season)
	if _, done := r.lookup(sk); done {
		return 0 // saison déjà lue : épisode inconnu de TMDB
	}
	var d struct {
		Episodes []struct {
			EpisodeNumber int `json:"episode_number"`
			Runtime       int `json:"runtime"`
		} `json:"episodes"`
	}
	if _, ok := r.fetch(fmt.Sprintf("tv/%d/season/%d", show, season), &d); !ok {
		return 0
	}
	vals := map[string]int{sk: 1}
	for _, e := range d.Episodes {
		vals[fmt.Sprintf("e%d/%d/%d", show, season, e.EpisodeNumber)] = e.Runtime
	}
	r.set(vals)
	v, _ := r.lookup(k)
	return v
}

func (r *tmdbRuntimes) save() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	b, err := json.Marshal(r.m)
	r.dirty = false
	r.mu.Unlock()
	if err == nil {
		tmp := r.path + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, r.path)
		}
	}
}
