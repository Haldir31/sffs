package main

// Catalogue de l'interface web : recherche des films et séries exposés, et liste des fichiers
// (versions) d'un titre, lue dans l'arborescence FUSE en mémoire. Rien n'est demandé à
// StreamFusion ni au débrideur.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
)

type catalogHit struct {
	Kind  string `json:"kind"` // film, série
	TMDB  int64  `json:"tmdb"`
	Dir   string `json:"dir"`
	Files int    `json:"files"`
}

type catalogFile struct {
	Name      string    `json:"name"`
	Size      uint64    `json:"size"`
	SizeExact bool      `json:"size_exact"`
	Bucket    string    `json:"bucket"`
	Remux     bool      `json:"remux,omitempty"`
	Seeders   int       `json:"seeders"`
	Hash      string    `json:"hash"`
	Added     time.Time `json:"added"`
	Probe     probeInfo `json:"probe"`
	Known     bool      `json:"known,omitempty"`     // vu en cache par sffs (14 jours)
	Community bool      `json:"community,omitempty"` // vu en cache par la communauté StreamFusion
	Served    string    `json:"served,omitempty"`    // remplaçante actuelle (version hors cache)
	Season    int       `json:"season,omitempty"`
	Episode   int       `json:"episode,omitempty"`
}

type catalogTitle struct {
	Kind    string        `json:"kind"`
	TMDB    int64         `json:"tmdb"`
	Dir     string        `json:"dir"`
	IMDb    string        `json:"imdb,omitempty"`
	Seasons []int         `json:"seasons,omitempty"`
	Counts  map[int]int   `json:"counts,omitempty"` // saison -> fichiers
	Season  int           `json:"season,omitempty"` // saison détaillée
	Files   []catalogFile `json:"files"`
}

// lockTree prend le verrou de l'arborescence, sans attendre plus de 3 s (un rechargement complet
// le garde plusieurs minutes).
func (n *rootNode) lockTree() bool {
	deadline := time.Now().Add(3 * time.Second)
	for !n.mu.TryLock() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

func countFiles(in *fs.Inode) int {
	if in == nil {
		return 0
	}
	if !in.IsDir() {
		return 1
	}
	n := 0
	for _, ch := range in.Children() {
		n += countFiles(ch)
	}
	return n
}

// searchCatalog : titres dont le nom contient tous les mots de q (sans accents ni ponctuation).
func (n *rootNode) searchCatalog(q, kind string, limit int) ([]catalogHit, bool) {
	words := strings.Fields(fold(q))
	if !n.lockTree() {
		return nil, false
	}
	var hits []catalogHit
	scan := func(k string, dirs map[int64]string) {
		for id, d := range dirs {
			f := fold(d)
			ok := true
			for _, w := range words {
				if !strings.Contains(f, w) {
					ok = false
					break
				}
			}
			if ok {
				hits = append(hits, catalogHit{Kind: k, TMDB: id, Dir: d})
			}
		}
	}
	if kind != "série" {
		scan("film", n.movieDirs)
	}
	if kind != "film" && n.series != nil {
		scan("série", n.showDirs)
	}
	n.mu.Unlock()
	// Titre commençant par la recherche d'abord, puis ordre alphabétique.
	prefix := strings.Join(words, " ")
	sort.Slice(hits, func(i, j int) bool {
		pi, pj := strings.HasPrefix(fold(hits[i].Dir), prefix), strings.HasPrefix(fold(hits[j].Dir), prefix)
		if pi != pj {
			return pi
		}
		return hits[i].Dir < hits[j].Dir
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	for i := range hits {
		parent := n.films
		if hits[i].Kind == "série" {
			parent = n.series
		}
		hits[i].Files = countFiles(parent.GetChild(hits[i].Dir))
	}
	return hits, true
}

func (n *rootNode) fileInfo(in *fs.Inode) (catalogFile, bool) {
	fn, ok := in.Operations().(*fileNode)
	if !ok {
		return catalogFile{}, false
	}
	v := fn.v
	f := catalogFile{Name: v.name, Size: v.size, SizeExact: v.sizeExact, Bucket: v.bucket, Remux: v.remux,
		Seeders: v.seeders, Hash: v.infoHash, Added: v.mtime, Season: v.season, Episode: v.episode,
		Community: community.has(v.infoHash)}
	_ = json.Unmarshal(v.probeJSON, &f.Probe)
	if v.imdb != "" {
		key := "movie/" + v.imdb
		if v.mediaType == "series" {
			key = "series/" + v.imdb + ":" + strconv.Itoa(v.season) + ":" + strconv.Itoa(v.episode)
		}
		f.Known = known.isKnown(key, v.infoHash)
	}
	n.st.res.mu.Lock()
	if v.sub != nil {
		f.Served = v.sub.file
	}
	n.st.res.mu.Unlock()
	return f, true
}

var seasonDir = regexp.MustCompile(`^Season (\d+)$`)

// catalogTitle : fichiers d'un film, ou d'une saison d'une série (season 0 = la première).
func (n *rootNode) catalogTitle(kind string, tmdb int64, season int) (*catalogTitle, bool) {
	if !n.lockTree() {
		return nil, false
	}
	dirs, parent := n.movieDirs, n.films
	if kind == "série" {
		dirs, parent = n.showDirs, n.series
	}
	name, ok := "", false
	if dirs != nil {
		name, ok = dirs[tmdb]
	}
	n.mu.Unlock()
	if !ok || parent == nil {
		return nil, true
	}
	dir := parent.GetChild(name)
	if dir == nil {
		return nil, true
	}
	t := &catalogTitle{Kind: kind, TMDB: tmdb, Dir: name, Files: []catalogFile{}}
	add := func(in *fs.Inode) {
		if f, ok := n.fileInfo(in); ok {
			t.Files = append(t.Files, f)
			if t.IMDb == "" {
				if fn, ok := in.Operations().(*fileNode); ok {
					t.IMDb = fn.v.imdb
				}
			}
		}
	}
	if kind == "film" {
		for _, ch := range dir.Children() {
			add(ch)
		}
	} else {
		t.Counts = map[int]int{}
		seasons := map[int]*fs.Inode{}
		for nm, ch := range dir.Children() {
			if m := seasonDir.FindStringSubmatch(nm); m != nil {
				s, _ := strconv.Atoi(m[1])
				seasons[s] = ch
				t.Seasons = append(t.Seasons, s)
				t.Counts[s] = len(ch.Children())
			}
		}
		sort.Ints(t.Seasons)
		if _, ok := seasons[season]; !ok && len(t.Seasons) > 0 {
			season = t.Seasons[0]
			if season == 0 && len(t.Seasons) > 1 { // saison 0 = épisodes spéciaux : pas par défaut
				season = t.Seasons[1]
			}
		}
		t.Season = season
		if sd := seasons[season]; sd != nil {
			for _, ch := range sd.Children() {
				add(ch)
			}
		}
	}
	order := map[string]int{"2160": 0, "1080": 1, "720": 2, "480": 3, "unknown": 4}
	sort.SliceStable(t.Files, func(i, j int) bool {
		a, b := t.Files[i], t.Files[j]
		if a.Episode != b.Episode {
			return a.Episode < b.Episode
		}
		if order[a.Bucket] != order[b.Bucket] {
			return order[a.Bucket] < order[b.Bucket]
		}
		if a.Remux != b.Remux {
			return a.Remux
		}
		return a.Seeders > b.Seeders
	})
	return t, true
}

func (u *uiServer) catalogSearch(w http.ResponseWriter, r *http.Request) {
	root := u.root()
	if root == nil {
		http.Error(w, "catalogue en cours de chargement", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	hits, ok := root.searchCatalog(q.Get("q"), q.Get("kind"), 60)
	if !ok {
		http.Error(w, "rechargement du catalogue en cours, réessayer", http.StatusServiceUnavailable)
		return
	}
	if hits == nil {
		hits = []catalogHit{}
	}
	writeJSON(w, hits)
}

func (u *uiServer) catalogTitle(w http.ResponseWriter, r *http.Request) {
	root := u.root()
	if root == nil {
		http.Error(w, "catalogue en cours de chargement", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	id, _ := strconv.ParseInt(q.Get("tmdb"), 10, 64)
	season, err := strconv.Atoi(q.Get("season"))
	if err != nil {
		season = -1 // saison par défaut
	}
	kind := q.Get("kind")
	if kind != "film" && kind != "série" {
		http.Error(w, "kind : film ou série", http.StatusBadRequest)
		return
	}
	t, ok := root.catalogTitle(kind, id, season)
	switch {
	case !ok:
		http.Error(w, "rechargement du catalogue en cours, réessayer", http.StatusServiceUnavailable)
	case t == nil:
		http.Error(w, "titre absent de sffs", http.StatusNotFound)
	default:
		writeJSON(w, t)
	}
}
