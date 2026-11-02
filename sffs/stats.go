package main

// Statistiques d'usage pour l'interface web (SFFS_UI) : catalogue, surveillance de StreamFusion,
// scans demandés à Silo, lectures et ressources. Tout est en mémoire, sauf l'historique des
// lectures, gardé sur disque (SFFS_UI_HISTORY) pour survivre à un redémarrage.

import (
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	historyMax = 2000                // lectures gardées
	historyAge = 30 * 24 * time.Hour // au-delà, oubliées
	passesMax  = 100                 // passages de surveillance gardés (ceux qui ont changé quelque chose)
	refusedMax = 50                  // derniers refus gardés (ouvertures, scans Silo)
)

// playEvent : une lecture, de l'armement par Silo à la dernière lecture d'octets.
type playEvent struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Path      string    `json:"path"` // films/<dossier>/<fichier> ou series/<dossier>/Season NN/<fichier>
	Caller    string    `json:"caller"`
	Status    string    `json:"status"` // cache, substitution, community, uncached, denied
	Served    string    `json:"served,omitempty"`
	Bucket    string    `json:"bucket,omitempty"` // qualité de la remplaçante
	ResolveMs int64     `json:"resolve_ms,omitempty"`
	Host      string    `json:"host,omitempty"`
	Bytes     int64     `json:"bytes"`
	Errors    int       `json:"errors,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	LastRead  time.Time `json:"last_read,omitzero"`
}

type watchPass struct {
	At      time.Time `json:"at"`
	Mode    string    `json:"mode"` // ciblé, complet
	Touched int       `json:"touched"`
	Changed int       `json:"changed"`
	Movies  int       `json:"movies"`
	Shows   int       `json:"shows"`
	Added   int       `json:"added"`
	Removed int       `json:"removed"`
	Dirs    int       `json:"dirs"`
	Ms      int64     `json:"ms"`
}

type loadInfo struct {
	At              time.Time `json:"at,omitzero"`
	Movies          int       `json:"movies"`
	MovieVersions   int       `json:"movie_versions"`
	Shows           int       `json:"shows"`
	Episodes        int       `json:"episodes"`
	EpisodeVersions int       `json:"episode_versions"`
	Ms              int64     `json:"ms"`
}

type reloadInfo struct {
	At      time.Time `json:"at,omitzero"`
	Added   int       `json:"added"`
	Removed int       `json:"removed"`
	Dirs    int       `json:"dirs"`
	Ms      int64     `json:"ms"`
	Error   string    `json:"error,omitempty"`
}

type refusal struct {
	At     time.Time `json:"at"`
	What   string    `json:"what"`
	Reason string    `json:"reason"`
}

type counters struct {
	Plays        int64 `json:"plays"`
	Cached       int64 `json:"cached"`
	Substituted  int64 `json:"substituted"`
	Community    int64 `json:"community"`
	Uncached     int64 `json:"uncached"`
	Denied       int64 `json:"denied"`
	Resolves     int64 `json:"resolves"`
	ResolveMsSum int64 `json:"resolve_ms_sum"`
	ResolveMsMax int64 `json:"resolve_ms_max"`
	ListFetches  int64 `json:"list_fetches"`
	ListErrors   int64 `json:"list_errors"`
	ListMsSum    int64 `json:"list_ms_sum"`
	BytesRead    int64 `json:"bytes_read"`
	ReadErrors   int64 `json:"read_errors"`
	OpenRefused  int64 `json:"open_refused"`
}

type siloStats struct {
	Requested   int64     `json:"requested"`
	Refused     int64     `json:"refused"`
	Failed      int64     `json:"failed"`
	RateLimited int64     `json:"rate_limited"`
	Pending     int       `json:"pending"`
	LastAt      time.Time `json:"last_at,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	Enabled     bool      `json:"enabled"`
}

type runtimeStats struct {
	CPUPercent float64   `json:"cpu_percent"`
	CPUSeconds float64   `json:"cpu_seconds"`
	RSS        uint64    `json:"rss"`
	HeapInuse  uint64    `json:"heap_inuse"`
	HeapSys    uint64    `json:"heap_sys"`
	Sys        uint64    `json:"sys"`
	GCCount    uint32    `json:"gc_count"`
	Goroutines int       `json:"goroutines"`
	Threads    int       `json:"threads"`
	GOMAXPROCS int       `json:"gomaxprocs"`
	GOMEMLIMIT string    `json:"gomemlimit,omitempty"`
	GoVersion  string    `json:"go_version"`
	SampledAt  time.Time `json:"sampled_at,omitzero"`
}

type statsStore struct {
	mu      sync.Mutex
	started time.Time
	phase   string // chargement, monté

	load        loadInfo
	reload      reloadInfo
	files       int // fichiers exposés (somme des ajouts et retraits)
	movieDirs   int
	showDirs    int
	watchEvery  time.Duration
	watchRuns   int64
	watchLast   time.Time
	watchErr    string
	passes      []watchPass
	pendingScan watchPass // contrôle en cours, complété par l'application

	silo    siloStats
	c       counters
	refused []refusal

	plays    []*playEvent          // du plus ancien au plus récent
	current  map[string]*playEvent // dernière lecture de chaque version
	dirty    bool
	histPath string

	rt runtimeStats
}

var stats = &statsStore{started: time.Now(), phase: "chargement", current: map[string]*playEvent{}}

func (s *statsStore) setPhase(p string) {
	s.mu.Lock()
	s.phase = p
	s.mu.Unlock()
}

// ---------------------------------------------------------------- catalogue

func (s *statsStore) noteLoad(movies []*movie, shows []*show, d time.Duration) {
	li := loadInfo{At: time.Now(), Movies: len(movies), Shows: len(shows), Ms: d.Milliseconds()}
	for _, m := range movies {
		li.MovieVersions += len(m.versions)
	}
	for _, sh := range shows {
		li.Episodes += sh.episodes
		for _, vs := range sh.seasons {
			li.EpisodeVersions += len(vs)
		}
	}
	s.mu.Lock()
	s.load = li
	s.mu.Unlock()
}

func (s *statsStore) noteReload(added, removed, dirs int, d time.Duration, err error) {
	ri := reloadInfo{At: time.Now(), Added: added, Removed: removed, Dirs: dirs, Ms: d.Milliseconds()}
	if err != nil {
		ri.Error = err.Error()
	}
	s.mu.Lock()
	s.reload = ri
	s.mu.Unlock()
}

// noteTree : fichiers ajoutés et retirés de l'arborescence, et nombre de dossiers de titres.
func (s *statsStore) noteTree(added, removed, movieDirs, showDirs int) {
	s.mu.Lock()
	s.files += added - removed
	s.movieDirs, s.showDirs = movieDirs, showDirs
	s.mu.Unlock()
}

// ---------------------------------------------------------------- surveillance

func (s *statsStore) noteWatchRun(err error) {
	s.mu.Lock()
	s.watchRuns++
	s.watchLast = time.Now()
	if err != nil {
		s.watchErr = err.Error()
	} else {
		s.watchErr = ""
	}
	s.mu.Unlock()
}

// noteWatchCheck : résultat du contrôle des empreintes (titres réécrits, titres modifiés).
func (s *statsStore) noteWatchCheck(touched, changed int, mode string) {
	s.mu.Lock()
	s.pendingScan = watchPass{At: time.Now(), Mode: mode, Touched: touched, Changed: changed}
	if changed == 0 && touched >= 50 { // gros passage sans effet : gardé pour l'historique
		s.pushPass(s.pendingScan)
		s.pendingScan = watchPass{}
	}
	s.mu.Unlock()
}

// noteWatchApply : rechargement ciblé qui a suivi le contrôle.
func (s *statsStore) noteWatchApply(movies, shows, added, removed, dirs int, d time.Duration) {
	s.mu.Lock()
	p := s.pendingScan
	if p.At.IsZero() {
		p.At = time.Now()
	}
	p.Movies, p.Shows, p.Added, p.Removed, p.Dirs, p.Ms = movies, shows, added, removed, dirs, d.Milliseconds()
	s.pushPass(p)
	s.pendingScan = watchPass{}
	s.mu.Unlock()
}

func (s *statsStore) pushPass(p watchPass) {
	s.passes = append(s.passes, p)
	if len(s.passes) > passesMax {
		s.passes = append(s.passes[:0:0], s.passes[len(s.passes)-passesMax:]...)
	}
}

// ---------------------------------------------------------------- Silo

func (s *statsStore) noteSilo(ok, failed int, limited bool, pending int, err error) {
	s.mu.Lock()
	s.silo.Requested += int64(ok)
	s.silo.Failed += int64(failed)
	if limited {
		s.silo.RateLimited++
	}
	s.silo.Pending = pending
	if ok > 0 || failed > 0 {
		s.silo.LastAt = time.Now()
	}
	if err != nil {
		s.silo.LastError = err.Error()
	} else if ok > 0 {
		s.silo.LastError = ""
	}
	s.mu.Unlock()
}

func (s *statsStore) noteSiloRefused(path string, status int) {
	s.mu.Lock()
	s.silo.Refused++
	s.pushRefusal(refusal{At: time.Now(), What: path, Reason: "scan Silo refusé (statut " + strconv.Itoa(status) + ")"})
	s.mu.Unlock()
}

func (s *statsStore) pushRefusal(r refusal) {
	s.refused = append(s.refused, r)
	if len(s.refused) > refusedMax {
		s.refused = append(s.refused[:0:0], s.refused[len(s.refused)-refusedMax:]...)
	}
}

// ---------------------------------------------------------------- lectures

// playStart : Silo arme un fichier (début d'une lecture).
func (s *statsStore) playStart(id, path, caller string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &playEvent{ID: id, At: time.Now(), Path: path, Caller: caller}
	s.plays = append(s.plays, p)
	s.current[id] = p
	s.c.Plays++
	s.trimLocked()
	s.dirty = true
}

// play renvoie la lecture en cours de cette version (nil hors lecture). Appelant : s.mu tenu.
func (s *statsStore) play(id string) *playEvent {
	p := s.current[id]
	if p == nil || time.Since(p.At) > 12*time.Hour {
		return nil
	}
	return p
}

// playStatus : verdict du cache pour une version (seulement pendant une lecture).
func (s *statsStore) playStatus(id, status, served, bucket string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.play(id)
	if p == nil || (p.Status == status && p.Served == served) {
		return
	}
	switch status {
	case "cache":
		s.c.Cached++
	case "substitution":
		s.c.Substituted++
	case "community":
		s.c.Community++
	case "uncached":
		s.c.Uncached++
	case "denied":
		s.c.Denied++
	}
	p.Status, p.Served, p.Bucket = status, served, bucket
	s.dirty = true
}

func (s *statsStore) noteResolve(id, host string, d time.Duration) {
	ms := d.Milliseconds()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.Resolves++
	s.c.ResolveMsSum += ms
	if ms > s.c.ResolveMsMax {
		s.c.ResolveMsMax = ms
	}
	if p := s.play(id); p != nil && p.ResolveMs == 0 {
		p.ResolveMs, p.Host = max(ms, 1), host
		s.dirty = true
	}
}

func (s *statsStore) noteRead(id string, n int) {
	s.mu.Lock()
	s.c.BytesRead += int64(n)
	if p := s.play(id); p != nil {
		p.Bytes += int64(n)
		p.LastRead = time.Now()
		s.dirty = true
	}
	s.mu.Unlock()
}

func (s *statsStore) noteReadError(id string, err error) {
	s.mu.Lock()
	s.c.ReadErrors++
	if p := s.play(id); p != nil {
		p.Errors++
		p.LastError = err.Error()
		s.dirty = true
	}
	s.mu.Unlock()
}

func (s *statsStore) noteList(d time.Duration, err error) {
	s.mu.Lock()
	s.c.ListFetches++
	s.c.ListMsSum += d.Milliseconds()
	if err != nil {
		s.c.ListErrors++
	}
	s.mu.Unlock()
}

func (s *statsStore) noteOpenRefused(name, caller, reason string) {
	s.mu.Lock()
	s.c.OpenRefused++
	s.pushRefusal(refusal{At: time.Now(), What: name, Reason: reason + " (" + caller + ")"})
	s.mu.Unlock()
}

// trimLocked oublie les lectures trop anciennes ou en surnombre. Appelant : s.mu tenu.
func (s *statsStore) trimLocked() {
	cut := 0
	for cut < len(s.plays) && (len(s.plays)-cut > historyMax || time.Since(s.plays[cut].At) > historyAge) {
		if s.current[s.plays[cut].ID] == s.plays[cut] {
			delete(s.current, s.plays[cut].ID)
		}
		cut++
	}
	if cut > 0 {
		s.plays = append(s.plays[:0:0], s.plays[cut:]...)
	}
}

// ---------------------------------------------------------------- historique sur disque

func (s *statsStore) loadHistory(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.histPath = path
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var ps []*playEvent
	if json.Unmarshal(b, &ps) != nil {
		return
	}
	s.plays = ps
	for _, p := range ps {
		s.current[p.ID] = p
	}
	s.trimLocked()
}

func (s *statsStore) saveHistory() {
	s.mu.Lock()
	if !s.dirty || s.histPath == "" {
		s.mu.Unlock()
		return
	}
	b, err := json.Marshal(s.plays)
	s.dirty = false
	path := s.histPath
	s.mu.Unlock()
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// ---------------------------------------------------------------- ressources

// sampleLoop mesure la consommation du processus et enregistre l'historique toutes les 10 s.
func (s *statsStore) sampleLoop() {
	var lastCPU float64
	var lastAt time.Time
	for {
		cpu := procCPUSeconds()
		now := time.Now()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		rss, threads := procStatus()
		rt := runtimeStats{
			CPUSeconds: cpu, RSS: rss, HeapInuse: ms.HeapInuse, HeapSys: ms.HeapSys, Sys: ms.Sys,
			GCCount: ms.NumGC, Goroutines: runtime.NumGoroutine(), Threads: threads,
			GOMAXPROCS: runtime.GOMAXPROCS(0), GOMEMLIMIT: os.Getenv("GOMEMLIMIT"), GoVersion: runtime.Version(),
			SampledAt: now,
		}
		if !lastAt.IsZero() && cpu >= lastCPU {
			rt.CPUPercent = (cpu - lastCPU) / now.Sub(lastAt).Seconds() * 100
		}
		lastCPU, lastAt = cpu, now
		s.mu.Lock()
		s.rt = rt
		s.mu.Unlock()
		s.saveHistory()
		time.Sleep(10 * time.Second)
	}
}

// procCPUSeconds : temps CPU (utilisateur + système) consommé par le processus.
func procCPUSeconds() float64 {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	// Les champs suivent le nom du programme, entre parenthèses (qui peut contenir des espaces).
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 13 {
		return 0
	}
	u, _ := strconv.ParseFloat(f[11], 64) // utime (14e champ)
	k, _ := strconv.ParseFloat(f[12], 64) // stime
	return (u + k) / 100                  // USER_HZ = 100 sous Linux
}

// procStatus : mémoire résidente (octets) et nombre de threads.
func procStatus() (rss uint64, threads int) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "VmRSS:":
			kb, _ := strconv.ParseUint(f[1], 10, 64)
			rss = kb << 10
		case "Threads:":
			threads, _ = strconv.Atoi(f[1])
		}
	}
	return rss, threads
}

// ---------------------------------------------------------------- instantané pour l'API

type statusView struct {
	Version string    `json:"version"`
	Started time.Time `json:"started"`
	Now     time.Time `json:"now"`
	Phase   string    `json:"phase"`
	Mount   string    `json:"mount"`
	Catalog struct {
		Load        loadInfo   `json:"load"`
		Reload      reloadInfo `json:"reload"`
		ReloadEvery string     `json:"reload_every"`
		Files       int        `json:"files"`
		MovieDirs   int        `json:"movie_dirs"`
		ShowDirs    int        `json:"show_dirs"`
	} `json:"catalog"`
	Watch struct {
		Every  string      `json:"every"`
		Runs   int64       `json:"runs"`
		Last   time.Time   `json:"last,omitzero"`
		Error  string      `json:"error,omitempty"`
		Passes []watchPass `json:"passes"`
	} `json:"watch"`
	Silo      siloStats `json:"silo"`
	Counters  counters  `json:"counters"`
	Refused   []refusal `json:"refused"`
	Community struct {
		Enabled bool      `json:"enabled"`
		Size    int       `json:"size"`
		Dropped int       `json:"dropped"`
		Loaded  time.Time `json:"loaded,omitzero"`
		Failed  bool      `json:"failed"`
		MaxAge  string    `json:"max_age"`
	} `json:"community"`
	KnownTitles int          `json:"known_titles"`
	Armed       int          `json:"armed"`
	Runtime     runtimeStats `json:"runtime"`
}

func (s *statsStore) view(cfg config, st *state) statusView {
	var v statusView
	v.Version = buildVersion()
	v.Now = time.Now()
	v.Mount = cfg.mount
	v.Catalog.ReloadEvery = durationOrOff(cfg.reload)
	s.mu.Lock()
	v.Started, v.Phase = s.started, s.phase
	v.Catalog.Load, v.Catalog.Reload = s.load, s.reload
	v.Catalog.Files, v.Catalog.MovieDirs, v.Catalog.ShowDirs = s.files, s.movieDirs, s.showDirs
	v.Watch.Every = durationOrOff(s.watchEvery)
	v.Watch.Runs, v.Watch.Last, v.Watch.Error = s.watchRuns, s.watchLast, s.watchErr
	v.Watch.Passes = append([]watchPass{}, s.passes...)
	v.Silo, v.Counters = s.silo, s.c
	v.Refused = append([]refusal{}, s.refused...)
	v.Runtime = s.rt
	s.mu.Unlock()

	community.mu.RLock()
	v.Community.Enabled, v.Community.Size, v.Community.Dropped = community.enabled, len(community.m), len(community.dropped)
	v.Community.Loaded, v.Community.Failed, v.Community.MaxAge = community.loaded, community.failed, community.maxAge.String()
	community.mu.RUnlock()

	known.mu.Lock()
	v.KnownTitles = len(known.m)
	known.mu.Unlock()

	if st != nil {
		st.mu.Lock()
		now := time.Now()
		for _, exp := range st.armed {
			if now.Before(exp) {
				v.Armed++
			}
		}
		st.mu.Unlock()
	}
	return v
}

// recentPlays : les n dernières lectures, de la plus récente à la plus ancienne.
func (s *statsStore) recentPlays(n int) []playEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.plays) {
		n = len(s.plays)
	}
	out := make([]playEvent, 0, n)
	for i := len(s.plays) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, *s.plays[i])
	}
	return out
}

func durationOrOff(d time.Duration) string {
	if d <= 0 {
		return "désactivé"
	}
	return humanDuration(d)
}

// humanDuration : « 24 h », « 1 min », « 1 h 30 min », « 45 s ».
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d/time.Hour), int(d%time.Hour/time.Minute), int(d%time.Minute/time.Second)
	var parts []string
	if h > 0 {
		parts = append(parts, strconv.Itoa(h)+" h")
	}
	if m > 0 {
		parts = append(parts, strconv.Itoa(m)+" min")
	}
	if s > 0 || len(parts) == 0 {
		parts = append(parts, strconv.Itoa(s)+" s")
	}
	return strings.Join(parts, " ")
}
