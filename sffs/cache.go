package main

// Cache AllDebrid, sans rien télécharger : on lit la liste de sources Stremio de StreamFusion
// pour le titre (ou l'épisode). StreamFusion y marque « ⚡instant » les torrents prêts chez le
// débrideur (il le vérifie lui-même, puis supprime aussitôt ses magnets de test).
//
//   - Une version marquée instant est lue telle quelle.
//   - Sinon, sffs la remplace pour cette lecture par une source instant de la même qualité (ou
//     de la plus proche, comme stremfin) : la lecture démarre sans téléchargement.
//   - Sans aucune source instant, la version est « hors cache » : sffs n'appelle jamais
//     /playback, qui lancerait le téléchargement, et Silo bascule sur une autre version.
//
// Les torrents vus instant sont retenus sur disque (known) : au rechargement suivant, la
// sélection les place en tête, pour que les versions proposées par Silo soient en cache.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var errNotCached = errors.New("pas en cache chez le débrideur")

const cacheTTL = 10 * time.Minute

// streamSrc est une source de la liste StreamFusion.
type streamSrc struct {
	hash    string
	instant bool
	bucket  string
	path    string // /playback/<config>/<requête>, à appeler sur SF_BASE
	file    string
	remux   bool
}

type cacheEntry struct {
	at      time.Time
	srcs    []streamSrc
	instant map[string]bool
	err     error
	done    chan struct{}
}

type cacheChecker struct {
	mu      sync.Mutex
	entries map[string]*cacheEntry
	client  *http.Client
}

func (r *resolver) streamKey(v *version) (kind, id string) {
	if v.mediaType == "series" {
		return "series", fmt.Sprintf("%s:%d:%d", v.imdb, v.season, v.episode)
	}
	return "movie", v.imdb
}

func (r *resolver) entry(v *version) *cacheEntry {
	kind, id := r.streamKey(v)
	key := kind + "/" + id
	cc := r.cc
	cc.mu.Lock()
	e := cc.entries[key]
	if e != nil {
		select {
		case <-e.done:
			if time.Since(e.at) > cacheTTL || e.err != nil {
				e = nil
			}
		default: // requête en cours : on l'attendra
		}
	}
	if e == nil {
		e = &cacheEntry{done: make(chan struct{})}
		cc.entries[key] = e
		cc.mu.Unlock()
		e.srcs, e.err = cc.fetch(r.cfg, kind, id)
		e.instant = map[string]bool{}
		var instant []string
		for _, s := range e.srcs {
			if s.instant {
				e.instant[s.hash] = true
				instant = append(instant, s.hash)
			}
		}
		e.at = time.Now()
		close(e.done)
		if e.err != nil {
			log.Printf("cache %s : %v", key, e.err)
		} else {
			log.Printf("cache %s : %d sources, %d en cache", key, len(e.srcs), len(instant))
			known.record(key, instant)
		}
		return e
	}
	cc.mu.Unlock()
	<-e.done
	return e
}

var fallbackBuckets = map[string][]string{
	"2160":    {"2160", "1080", "720", "480", "unknown"},
	"1080":    {"1080", "720", "2160", "480", "unknown"},
	"720":     {"720", "1080", "480", "2160", "unknown"},
	"480":     {"480", "720", "1080", "2160", "unknown"},
	"unknown": {"1080", "720", "2160", "480", "unknown"},
}

// cached indique si la version peut être lue sans téléchargement, en la remplaçant si besoin
// par une source en cache de qualité proche.
func (r *resolver) cached(ctx context.Context, v *version) bool {
	if v.imdb == "" {
		return false
	}
	e := r.entry(v)
	if e.err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.instant[v.infoHash] {
		if v.sub != nil { // l'original est (de nouveau) en cache : on revient à lui
			v.sub = nil
			v.size, v.sizeExact = v.origSize, v.origExact
			delete(r.cache, v.id)
		}
		return true
	}
	if v.sub != nil && e.instant[v.sub.hash] {
		return true
	}
	// Un REMUX hors cache est remplacé d'abord par un autre REMUX en cache de même qualité.
	for i := range e.srcs {
		s := e.srcs[i]
		if v.remux && s.instant && s.remux && s.bucket == v.bucket {
			log.Printf("substitution %s %q -> %s %q (%s, remux)", v.id, v.name, s.hash[:8], s.file, s.bucket)
			v.sub = &s
			v.sizeExact = false
			delete(r.cache, v.id)
			return true
		}
	}
	orig := codecOfVersion(v)
	for _, b := range fallbackBuckets[v.bucket] {
		best := -1
		for i := range e.srcs {
			s := e.srcs[i]
			if s.instant && s.bucket == b && (best < 0 || substituteRank(orig, s.file) < substituteRank(orig, e.srcs[best].file)) {
				best = i
			}
		}
		if best >= 0 {
			s := e.srcs[best]
			log.Printf("substitution %s %q -> %s %q (%s)", v.id, v.name, s.hash[:8], s.file, b)
			v.sub = &s
			v.sizeExact = false // taille réelle apprise juste après
			delete(r.cache, v.id)
			return true
		}
	}
	return false
}

var (
	reAV1  = regexp.MustCompile(`(?i)\bav1\b`)
	reHEVC = regexp.MustCompile(`(?i)(x265|h\.?265|hevc)`)
	reAVC  = regexp.MustCompile(`(?i)(x264|h\.?264|\bavc\b)`)
)

// codecOfName devine le codec vidéo d'une release d'après son nom (vide si inconnu).
func codecOfName(name string) string {
	switch {
	case reAV1.MatchString(name):
		return "av1"
	case reHEVC.MatchString(name):
		return "hevc"
	case reAVC.MatchString(name):
		return "h264"
	}
	return ""
}

func codecOfVersion(v *version) string {
	var pi probeInfo
	if json.Unmarshal(v.probeJSON, &pi) == nil && pi.CodecVideo != "" {
		return pi.CodecVideo
	}
	return codecOfName(v.name)
}

// substituteRank classe les remplaçantes d'une même qualité (plus petit = préférée) : même codec
// que l'original, puis H.264/HEVC, l'AV1 en dernier (peu de lecteurs le décodent en matériel,
// Apple TV compris), sauf si l'original est lui-même en AV1. À égalité, l'ordre de StreamFusion.
func substituteRank(orig, file string) int {
	c := codecOfName(file)
	switch {
	case c != "" && c == orig:
		return 0
	case c == "h264" || c == "hevc":
		return 1
	case c == "":
		return 2
	}
	return 3 // av1 (ou autre) alors que l'original ne l'est pas
}

// servedHash renvoie le hash du torrent réellement lu pour cette version (la remplaçante s'il y en a une).
func (r *resolver) servedHash(v *version) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v.sub != nil {
		return v.sub.hash
	}
	return v.infoHash
}

// substituted indique si la version est actuellement servie par une autre source.
func (r *resolver) substituted(v *version) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return v.sub != nil
}

// markUncached retire une source de la liste « en cache » (StreamFusion s'est trompé).
func (r *resolver) markUncached(v *version) {
	kind, id := r.streamKey(v)
	hash := v.infoHash
	r.mu.Lock()
	if v.sub != nil {
		hash = v.sub.hash
		v.sub = nil
	}
	r.mu.Unlock()
	r.cc.mu.Lock()
	if e := r.cc.entries[kind+"/"+id]; e != nil {
		select {
		case <-e.done:
			delete(e.instant, hash)
			for i := range e.srcs {
				if e.srcs[i].hash == hash {
					e.srcs[i].instant = false
				}
			}
		default:
		}
	}
	r.cc.mu.Unlock()
}

var resInName = regexp.MustCompile(`(?i)(2160p|4k|1080p|720p|576p|480p)`)

func (cc *cacheChecker) fetch(cfg config, kind, id string) ([]streamSrc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s/stream/%s/%s.json", cfg.base, cfg.streamCfg, kind, id), nil)
	resp, err := cc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("statut %d", resp.StatusCode)
	}
	var d struct {
		Streams []struct {
			Name          string `json:"name"`
			URL           string `json:"url"`
			InfoHash      string `json:"infoHash"`
			BehaviorHints struct {
				Filename string `json:"filename"`
			} `json:"behaviorHints"`
		} `json:"streams"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	var out []streamSrc
	for _, s := range d.Streams {
		h := strings.ToLower(strings.TrimSpace(s.InfoHash))
		u, err := url.Parse(s.URL)
		if h == "" || err != nil || !strings.HasPrefix(u.Path, "/playback/") {
			continue
		}
		b := "unknown"
		if m := resInName.FindString(s.Name); m != "" {
			b = bucketOf(m, "")
		}
		out = append(out, streamSrc{
			hash:    h,
			instant: strings.Contains(strings.ToLower(s.Name), "instant"),
			bucket:  b,
			path:    u.EscapedPath(),
			file:    s.BehaviorHints.Filename,
			remux:   isRemux(s.Name, s.BehaviorHints.Filename),
		})
	}
	return out, nil
}

// ---------------------------------------------------------------- cache appris

const knownTTL = 14 * 24 * time.Hour

type knownStore struct {
	path string
	mu   sync.Mutex
	m    map[string]map[string]int64 // clé de titre -> info_hash -> vu instant (unix)
}

var known = &knownStore{m: map[string]map[string]int64{}}

func loadKnown(path string) {
	known.path = path
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &known.m)
	}
}

func (k *knownStore) record(key string, hashes []string) {
	if len(hashes) == 0 {
		return
	}
	now := time.Now().Unix()
	k.mu.Lock()
	if k.m[key] == nil {
		k.m[key] = map[string]int64{}
	}
	for _, h := range hashes {
		k.m[key][h] = now
	}
	b, err := json.Marshal(k.m)
	k.mu.Unlock()
	if err == nil && k.path != "" {
		tmp := k.path + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, k.path)
		}
	}
}

// isKnown indique si le torrent a été vu en cache récemment pour ce titre.
func (k *knownStore) isKnown(key, hash string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	t, ok := k.m[key][hash]
	return ok && time.Since(time.Unix(t, 0)) < knownTTL
}
