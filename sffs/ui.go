package main

// Interface web de sffs (SFFS_UI=127.0.0.1:<port>) : tableau de bord, historique des lectures et
// réglages actifs, en lecture seule. Une page et son script sont intégrés au binaire (ui/).
//
// Accès par mot de passe (SFFS_UI_PASSWORD ou SFFS_UI_PASSWORD_FILE). Sans mot de passe, sffs en
// crée un au premier démarrage, l'enregistre à côté de l'historique (ui-password, 0600) et
// l'indique dans le journal : même en écoute locale, un serveur partagé laisse les autres
// utilisateurs joindre 127.0.0.1.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed ui
var uiFiles embed.FS

const (
	sessionCookie = "sffs_session"
	sessionTTL    = 30 * 24 * time.Hour
)

type uiServer struct {
	cfg      config
	wc       watchConfig
	key      []byte // clé de signature des sessions, tirée du mot de passe
	password string
	defaults map[string]string // valeurs par défaut calculées au démarrage (chemins)

	mu    sync.Mutex
	st    *state // nil tant que le montage n'est pas prêt
	fails []time.Time
}

// startUI lance l'interface si SFFS_UI est défini. dataDir : dossier des caches (historique, mot de passe).
func startUI(cfg config, wc watchConfig, dataDir string) *uiServer {
	addr := strings.TrimSpace(os.Getenv("SFFS_UI"))
	if addr == "" {
		return nil
	}
	stats.loadHistory(envOr("SFFS_UI_HISTORY", filepath.Join(dataDir, "play-history.json")))
	stats.loadChanges(filepath.Join(dataDir, "title-changes.json"))
	pw, err := uiPassword(dataDir)
	if err != nil {
		log.Fatalf("interface web : %v", err)
	}
	u := &uiServer{cfg: cfg, wc: wc, password: pw, defaults: map[string]string{
		"SFFS_KNOWN_CACHE":      filepath.Join(dataDir, "known-cached.json"),
		"SFFS_UI_HISTORY":       filepath.Join(dataDir, "play-history.json"),
		"SFFS_UI_PASSWORD_FILE": filepath.Join(dataDir, "ui-password"),
		"GOMAXPROCS":            strconv.Itoa(runtime.GOMAXPROCS(0)),
	}}
	sum := sha256.Sum256([]byte("sffs-ui\x00" + pw))
	u.key = sum[:]

	static, _ := fs.Sub(uiFiles, "ui")
	mux := http.NewServeMux()
	mux.Handle("GET /", u.headers(http.FileServerFS(static)))
	mux.HandleFunc("POST /api/login", u.login)
	mux.HandleFunc("POST /api/logout", u.logout)
	mux.HandleFunc("GET /api/session", u.session)
	mux.Handle("GET /api/status", u.auth(u.status))
	mux.Handle("GET /api/plays", u.auth(u.plays))
	mux.Handle("GET /api/config", u.auth(u.config))
	mux.Handle("GET /api/changes", u.auth(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		writeJSON(w, stats.recentChanges(n))
	}))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("interface web : écoute %s : %v", addr, err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		if err := srv.Serve(ln); err != nil {
			log.Printf("interface web arrêtée : %v", err)
		}
	}()
	go stats.sampleLoop()
	log.Printf("interface web sur http://%s", ln.Addr())
	return u
}

func (u *uiServer) setState(st *state) {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.st = st
	u.mu.Unlock()
}

// uiPassword : SFFS_UI_PASSWORD, sinon SFFS_UI_PASSWORD_FILE, sinon <dataDir>/ui-password (créé au besoin).
func uiPassword(dataDir string) (string, error) {
	if pw := os.Getenv("SFFS_UI_PASSWORD"); pw != "" {
		return pw, nil
	}
	file := os.Getenv("SFFS_UI_PASSWORD_FILE")
	generated := file == ""
	if generated {
		file = filepath.Join(dataDir, "ui-password")
	}
	if b, err := os.ReadFile(file); err == nil {
		if pw := strings.TrimSpace(string(b)); pw != "" {
			return pw, nil
		}
	} else if !generated {
		return "", err
	}
	if !generated {
		return "", errEmptyPassword(file)
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	pw := hex.EncodeToString(b)
	if err := os.WriteFile(file, []byte(pw+"\n"), 0o600); err != nil {
		return "", err
	}
	log.Printf("interface web : mot de passe créé dans %s", file)
	return pw, nil
}

type errEmptyPassword string

func (e errEmptyPassword) Error() string { return "fichier de mot de passe vide : " + string(e) }

// ---------------------------------------------------------------- sessions

func (u *uiServer) sign(exp int64) string {
	m := hmac.New(sha256.New, u.key)
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(m.Sum(nil))
}

func (u *uiServer) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, _, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > n {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(u.sign(n)))
}

// tooManyFails : plus de 5 échecs de connexion dans la dernière minute.
func (u *uiServer) tooManyFails() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	kept := u.fails[:0]
	for _, t := range u.fails {
		if time.Since(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	u.fails = kept
	return len(u.fails) >= 5
}

func (u *uiServer) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	if u.tooManyFails() {
		http.Error(w, "trop d'essais, réessayer dans une minute", http.StatusTooManyRequests)
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.Password), []byte(u.password)) != 1 {
		u.mu.Lock()
		u.fails = append(u.fails, time.Now())
		u.mu.Unlock()
		time.Sleep(time.Second)
		http.Error(w, "mot de passe incorrect", http.StatusUnauthorized)
		return
	}
	exp := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: u.sign(exp.Unix()), Path: "/", Expires: exp,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	w.WriteHeader(http.StatusNoContent)
}

func (u *uiServer) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (u *uiServer) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"authenticated": u.validSession(r)})
}

func (u *uiServer) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !u.validSession(r) {
			http.Error(w, "connexion requise", http.StatusUnauthorized)
			return
		}
		h(w, r)
	})
}

func (u *uiServer) headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------- API

func (u *uiServer) status(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	st := u.st
	u.mu.Unlock()
	writeJSON(w, stats.view(u.cfg, st))
}

func (u *uiServer) plays(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, stats.recentPlays(n))
}

type configEntry struct {
	Group  string `json:"group"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	Set    bool   `json:"set"` // défini dans l'environnement (sinon valeur par défaut)
	Secret bool   `json:"secret,omitempty"`
	Desc   string `json:"desc"`
}

type configDef struct {
	group, name, def, desc string
	secret                 bool
}

// Réglages présentés dans l'interface, dans l'ordre d'affichage.
var configDefs = []configDef{
	{group: "StreamFusion", name: "SF_BASE", desc: "Adresse de StreamFusion (listes de sources et liens de lecture)"},
	{group: "StreamFusion", name: "SF_PG_DSN", desc: "Base StreamFusion, en lecture seule (mot de passe masqué)"},
	{group: "StreamFusion", name: "SF_PLAYBACK_CFG", secret: true, desc: "Configuration StreamFusion des liens /playback"},
	{group: "StreamFusion", name: "SF_STREAM_CFG", secret: true, desc: "Configuration Stremio chiffrée (sources en cache)"},
	{group: "StreamFusion", name: "SFFS_DEBRID_PROXY", desc: "Proxy des téléchargements AllDebrid (WARP)"},

	{group: "Catalogue", name: "SFFS_SERIES", def: "0", desc: "Exposer aussi les séries"},
	{group: "Catalogue", name: "SFFS_ALL_VERSIONS", def: "0", desc: "Toutes les versions lisibles (ni quota, ni filtre de langue ou d'encodage léger)"},
	{group: "Catalogue", name: "SFFS_LANGS", def: "fr", desc: "Langues préférées, dans l'ordre"},
	{group: "Catalogue", name: "SFFS_PER_QUALITY", def: "5", desc: "Versions de film gardées par qualité (0 = sans limite)"},
	{group: "Catalogue", name: "SFFS_SERIES_PER_QUALITY", def: "2", desc: "Versions d'épisode gardées par qualité (0 = sans limite)"},
	{group: "Catalogue", name: "SFFS_REQUIRE_PREFERRED_HD", def: "0", desc: "Écarter les films sans version HD dans une langue préférée"},
	{group: "Catalogue", name: "SFFS_CACHED_ONLY", def: "0", desc: "Seulement les versions vues en cache"},
	{group: "Catalogue", name: "SFFS_REMUX", def: "1", desc: "Garder les REMUX"},
	{group: "Catalogue", name: "SFFS_LIGHT", def: "0", desc: "Garder les encodages légers (HDLight, 4KLight, mHD…)"},
	{group: "Catalogue", name: "SFFS_TMDB", desc: "Films limités à ces identifiants TMDB (vide = tous)"},
	{group: "Catalogue", name: "SFFS_SERIES_TMDB", desc: "Séries limitées à ces identifiants TMDB (vide = toutes)"},
	{group: "Catalogue", name: "SFFS_TMDB_KEY", secret: true, desc: "Clé TMDB (épisodes de chaque saison)"},

	{group: "Mises à jour", name: "SFFS_WATCH", def: "1m", desc: "Suivi des nouveautés de StreamFusion (0 = désactivé)"},
	{group: "Mises à jour", name: "SFFS_RELOAD", def: "24h", desc: "Rechargement complet (0 = jamais)"},
	{group: "Mises à jour", name: "SFFS_COMMUNITY_CACHE", def: "1", desc: "Cache communautaire de StreamFusion en complément des vérifications"},
	{group: "Mises à jour", name: "SFFS_COMMUNITY_MAX_AGE", def: "168h", desc: "Âge maximal d'une vérification communautaire"},

	{group: "Silo", name: "SFFS_SILO_URL", desc: "Adresse de Silo (scans ciblés)"},
	{group: "Silo", name: "SFFS_SILO_KEY", secret: true, desc: "Clé d'API admin de Silo"},
	{group: "Silo", name: "SFFS_SILO_KEY_FILE", desc: "Fichier de la clé d'API Silo"},
	{group: "Silo", name: "SFFS_SILO_FILMS", def: "/mnt/sf/films", desc: "Dossier des films vu par Silo"},
	{group: "Silo", name: "SFFS_SILO_SERIES", def: "/mnt/sf/series", desc: "Dossier des séries vu par Silo"},
	{group: "Silo", name: "SFFS_SILO_LIBRARIES", desc: "Création automatique des bibliothèques Silo"},

	{group: "Montage et lecture", name: "SFFS_MOUNT", desc: "Point de montage FUSE"},
	{group: "Montage et lecture", name: "SFFS_ALLOW_COMM", def: ".", desc: "Programmes autorisés à lire (expression régulière)"},
	{group: "Montage et lecture", name: "SFFS_ARM_TTL", def: "6h", desc: "Durée de validité d'un armement"},
	{group: "Montage et lecture", name: "SFFS_ALLOW_OTHER", def: "0", desc: "Fichiers lisibles par les autres utilisateurs"},
	{group: "Montage et lecture", name: "SFFS_KNOWN_CACHE", desc: "Fichier des sources déjà vues en cache"},
	{group: "Montage et lecture", name: "SFFS_TMDB_CACHE", desc: "Cache disque des réponses TMDB"},

	{group: "Interface", name: "SFFS_UI", desc: "Adresse d'écoute de cette interface"},
	{group: "Interface", name: "SFFS_UI_PASSWORD", secret: true, desc: "Mot de passe de l'interface"},
	{group: "Interface", name: "SFFS_UI_PASSWORD_FILE", desc: "Fichier du mot de passe"},
	{group: "Interface", name: "SFFS_UI_HISTORY", desc: "Fichier de l'historique des lectures"},

	{group: "Ressources", name: "GOMAXPROCS", desc: "Cœurs utilisés par Go"},
	{group: "Ressources", name: "GOMEMLIMIT", desc: "Plafond mémoire du ramasse-miettes"},
}

func (u *uiServer) config(w http.ResponseWriter, r *http.Request) {
	out := make([]configEntry, 0, len(configDefs))
	for _, d := range configDefs {
		val, set := os.LookupEnv(d.name)
		e := configEntry{Group: d.group, Name: d.name, Set: set && val != "", Secret: d.secret, Desc: d.desc}
		switch {
		case !e.Set:
			e.Value = d.def
			if v, ok := u.defaults[d.name]; ok {
				e.Value = v
			}
		case d.secret:
			e.Value = "défini (masqué)"
		default:
			e.Value = maskValue(d.name, val)
		}
		out = append(out, e)
	}
	writeJSON(w, out)
}

var dsnPassword = regexp.MustCompile(`(?i)(password\s*=\s*)('[^']*'|\S+)`)

// maskValue masque les secrets d'une valeur affichable (mot de passe d'une DSN ou d'une URL de proxy).
func maskValue(name, v string) string {
	switch name {
	case "SF_PG_DSN", "SFFS_DEBRID_PROXY", "SF_BASE", "SFFS_SILO_URL":
		if u, err := url.Parse(v); err == nil && u.Scheme != "" && u.User != nil {
			return u.Redacted()
		}
		return dsnPassword.ReplaceAllString(v, "${1}xxxxx")
	}
	return v
}

func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "inconnue"
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "inconnue"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += " (modifiée)"
	}
	return rev
}
