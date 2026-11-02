package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMaskValue(t *testing.T) {
	cases := map[string]string{
		"postgres://sf:secret@127.0.0.1:5432/sf":           "postgres://sf:xxxxx@127.0.0.1:5432/sf",
		"host=/run/sfpg user=sf password=secret dbname=sf": "host=/run/sfpg user=sf password=xxxxx dbname=sf",
		"host=x password='a b' dbname=sf":                  "host=x password=xxxxx dbname=sf",
	}
	for in, want := range cases {
		if got := maskValue("SF_PG_DSN", in); got != want {
			t.Errorf("maskValue(%q) = %q, attendu %q", in, got, want)
		}
	}
	if got := maskValue("SFFS_DEBRID_PROXY", "socks5://u:p@warp:1080"); strings.Contains(got, ":p@") {
		t.Errorf("mot de passe du proxy visible : %q", got)
	}
}

func TestSecretsMasked(t *testing.T) {
	t.Setenv("SF_STREAM_CFG", "gAAAAsecret")
	t.Setenv("SFFS_SILO_KEY", "clé-silo")
	t.Setenv("SF_PG_DSN", "postgres://sf:motdepasse@h/sf")
	u := &uiServer{}
	rec := httptest.NewRecorder()
	u.config(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	body := rec.Body.String()
	for _, s := range []string{"gAAAAsecret", "clé-silo", "motdepasse"} {
		if strings.Contains(body, s) {
			t.Errorf("secret %q présent dans /api/config", s)
		}
	}
}

func TestSessions(t *testing.T) {
	u := &uiServer{key: []byte("clé"), password: "pw"}
	req := func(v string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
		return r
	}
	exp := time.Now().Add(time.Hour).Unix()
	if !u.validSession(req(u.sign(exp))) {
		t.Fatal("session valide refusée")
	}
	if u.validSession(req(u.sign(time.Now().Add(-time.Minute).Unix()))) {
		t.Error("session expirée acceptée")
	}
	forged := strconv.FormatInt(exp+3600, 10) + u.sign(exp)[len(strconv.FormatInt(exp, 10)):]
	if u.validSession(req(forged)) {
		t.Error("session falsifiée acceptée")
	}
	other := &uiServer{key: []byte("autre")}
	if other.validSession(req(u.sign(exp))) {
		t.Error("session signée avec une autre clé acceptée")
	}

	// Connexion : mauvais puis bon mot de passe, et API protégée.
	rec := httptest.NewRecorder()
	u.login(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"pw"}`)))
	if rec.Code != http.StatusNoContent || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("connexion : statut %d", rec.Code)
	}
	h := u.auth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("API sans session : statut %d", rec2.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	r.AddCookie(rec.Result().Cookies()[0])
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, r)
	if rec3.Code != http.StatusTeapot {
		t.Errorf("API avec session : statut %d", rec3.Code)
	}
}

func TestGeneratedPassword(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SFFS_UI_PASSWORD", "")
	t.Setenv("SFFS_UI_PASSWORD_FILE", "")
	pw, err := uiPassword(dir)
	if err != nil || len(pw) < 16 {
		t.Fatalf("mot de passe créé : %q, %v", pw, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "ui-password"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("fichier du mot de passe : %v %v", fi, err)
	}
	again, _ := uiPassword(dir)
	if again != pw {
		t.Error("le mot de passe doit être relu, pas recréé")
	}
}

func TestPlayHistory(t *testing.T) {
	s := &statsStore{current: map[string]*playEvent{}}
	s.playStart("a", "films/X (2020) {tmdb-1}/x.mkv", "silo")
	s.playStatus("a", "substitution", "y.mkv", "2160")
	s.playStatus("a", "substitution", "y.mkv", "2160") // même verdict : compté une fois
	s.noteResolve("a", "h.debrid.it", 120*time.Millisecond)
	s.noteOpen("a", "silo", 42, 10<<20)
	s.noteRead("a", 1<<20, 1<<20, 10*time.Millisecond)
	s.noteRead("a", 1<<20, 2<<20, time.Second) // blocage
	s.playStatus("inconnu", "cache", "", "")   // hors lecture : ignoré
	if s.c.Substituted != 1 || s.c.Cached != 0 || s.c.Plays != 1 {
		t.Fatalf("compteurs : %+v", s.c)
	}
	p := s.recentPlays(1)[0]
	if p.Status != "substitution" || p.Served != "y.mkv" || p.ResolveMs != 120 || p.Bytes != 2<<20 || p.Stalls != 1 || p.Position != 2<<20 || p.Process != "silo" || p.FirstByteMs == 0 {
		t.Fatalf("lecture : %+v", p)
	}

	path := filepath.Join(t.TempDir(), "h.json")
	s.histPath = path
	s.saveHistory()
	s2 := &statsStore{current: map[string]*playEvent{}}
	s2.loadHistory(path)
	if got := s2.recentPlays(0); len(got) != 1 || got[0].Served != "y.mkv" {
		t.Fatalf("historique relu : %+v", got)
	}

	// Trop de lectures ou trop anciennes : oubliées.
	s3 := &statsStore{current: map[string]*playEvent{}}
	s3.plays = append(s3.plays, &playEvent{ID: "vieux", At: time.Now().Add(-historyAge - time.Hour)})
	for i := 0; i < historyMax+10; i++ {
		s3.playStart(strconv.Itoa(i), "films/x", "silo")
	}
	if len(s3.plays) != historyMax || s3.plays[0].ID != "10" {
		t.Fatalf("élagage : %d lectures, première %q", len(s3.plays), s3.plays[0].ID)
	}
}

func TestProcSamples(t *testing.T) {
	if procCPUSeconds() < 0 {
		t.Error("temps CPU négatif")
	}
	if rss, threads := procStatus(); rss == 0 || threads == 0 {
		t.Errorf("procStatus : rss=%d threads=%d", rss, threads)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		24 * time.Hour: "24 h", time.Minute: "1 min", 90 * time.Minute: "1 h 30 min", 45 * time.Second: "45 s", 0: "0 s",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, attendu %q", d, got, want)
		}
	}
}

func TestPlayStatusIfUnset(t *testing.T) {
	s := &statsStore{current: map[string]*playEvent{}}
	s.playStart("a", "films/x", "silo")
	s.playStatusIfUnset("a", "cache", "", "")
	s.playStatusIfUnset("a", "substitution", "y", "1080") // déjà un verdict : inchangé
	if p := s.recentPlays(1)[0]; p.Status != "cache" || s.c.Cached != 1 || s.c.Substituted != 0 {
		t.Fatalf("verdict : %+v %+v", p, s.c)
	}
}

func TestSessionMatches(t *testing.T) {
	armed := time.Now()
	ip := func(n int) *int { return &n }
	ep := &version{mediaType: "series", season: 3, episode: 5}
	dir := "series/Gilmore Girls (2000) {tmdb-4586}/Season 03/Gilmore Girls - S03E05 - x.mkv"
	s := siloSession{SeriesName: "Gilmore Girls", SeasonNumber: ip(3), EpisodeNumber: ip(5), StartedAt: armed.Add(2 * time.Second)}
	if !sessionMatches(s, ep, dir, armed) {
		t.Error("épisode non reconnu")
	}
	s.EpisodeNumber = ip(6)
	if sessionMatches(s, ep, dir, armed) {
		t.Error("autre épisode accepté")
	}
	mv := &version{mediaType: "movie"}
	m := siloSession{MediaTitle: "L'Été meurtrier", MediaType: "movie", StartedAt: armed}
	if !sessionMatches(m, mv, "films/L Ete meurtrier (1983) {tmdb-1}/x.mkv", armed) {
		t.Error("film (accents, ponctuation) non reconnu")
	}
	m.StartedAt = armed.Add(-10 * time.Minute)
	if sessionMatches(m, mv, "films/L Ete meurtrier (1983) {tmdb-1}/x.mkv", armed) {
		t.Error("session trop ancienne acceptée")
	}
	c := clientOf(siloSession{ClientName: "Infuse", ClientVersion: "8.1", Username: "admin", ProfileName: "Enfants", PlayMethod: "direct"})
	if c.App != "Infuse 8.1" || c.User != "admin (Enfants)" {
		t.Errorf("client : %+v", c)
	}
}

func TestTitleChanges(t *testing.T) {
	s := &statsStore{current: map[string]*playEvent{}}
	s.noteWatchCheck(3, 2, "ciblé")
	s.noteTitleChanges([]titleChange{{Kind: "film", Dir: "A (2020) {tmdb-1}", State: "nouveau", Added: 3}, {Kind: "série", Dir: "B {tmdb-2}", State: "retiré", Removed: 9}})
	s.noteWatchApply(1, 1, 3, 9, 2, time.Second)
	if len(s.passes) != 1 || len(s.passes[0].Titles) != 2 || s.passes[0].Titles[0].At.IsZero() {
		t.Fatalf("passage : %+v", s.passes)
	}
	if c := s.recentChanges(1); len(c) != 1 || c[0].Dir != "B {tmdb-2}" {
		t.Fatalf("derniers changements : %+v", c)
	}
	path := filepath.Join(t.TempDir(), "c.json")
	s.changesPath = path
	s.saveChanges()
	s2 := &statsStore{}
	s2.loadChanges(path)
	if len(s2.changes) != 2 {
		t.Fatalf("relu : %+v", s2.changes)
	}
}
