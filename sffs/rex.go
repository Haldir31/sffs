package main

// Source rex (https://github.com/debridmediamanager/rex) : rex se présente aux applications Plex
// comme un serveur Plex qui ne possède aucun fichier. sffs lui envoie son catalogue par l'API
// d'alimentation (/rex/feed) et lui sert les octets par HTTP, relayés depuis le débrideur au
// moment d'une lecture. Aucun garde-fou n'est nécessaire : rex n'analyse rien, il ne lit un
// fichier que pour une lecture (ou une conversion).
//
//	SFFS_REX_URL         adresse de rex, ex. http://127.0.0.1:32543 (vide : source rex coupée)
//	SFFS_REX_SOURCE      nom de la source dans rex (défaut sffs)
//	SFFS_REX_LISTEN      écoute du serveur d'octets (défaut 127.0.0.1:32545)
//	SFFS_REX_BYTES_URL   adresse du serveur d'octets vue par rex (défaut http://<SFFS_REX_LISTEN>)
//	SFFS_REX_TOKEN       jeton exigé par le serveur d'octets (défaut : SFFS_REX_TOKEN_FILE, créé au besoin)
//	SFFS_REX_STATE       dossier des analyses ffprobe faites à la lecture (défaut ~/.cache/sffs-rex)
//	SFFS_REX_PROBE       0 : pas d'analyse ffprobe à la première lecture (défaut 1)
//	SFFS_FFPROBE         binaire ffprobe (défaut ffprobe)
//
// Infos techniques : rex exige pour chaque fichier un résultat au format ffprobe. Faute de
// mieux, sffs le fabrique d'après ce que StreamFusion déduit du nom de la release (codecs,
// résolution, HDR, langues ; ni durée ni pistes réelles). À la première lecture, sffs analyse la
// source réellement servie avec ffprobe, garde le résultat sur disque et l'envoie à rex, qui met
// le fichier à jour sans changer de clé (historique de visionnage conservé).

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
)

type rexConfig struct {
	url, source, listen, bytesURL, token, stateDir, ffprobe string
	probe                                                   bool
}

func loadRexConfig() (rexConfig, error) {
	c := rexConfig{
		url:      strings.TrimRight(os.Getenv("SFFS_REX_URL"), "/"),
		source:   envOr("SFFS_REX_SOURCE", "sffs"),
		listen:   envOr("SFFS_REX_LISTEN", "127.0.0.1:32545"),
		ffprobe:  envOr("SFFS_FFPROBE", "ffprobe"),
		probe:    os.Getenv("SFFS_REX_PROBE") != "0",
		stateDir: os.Getenv("SFFS_REX_STATE"),
	}
	if c.url == "" {
		return c, nil
	}
	if c.stateDir == "" {
		d, err := os.UserCacheDir()
		if err != nil {
			return c, err
		}
		c.stateDir = filepath.Join(d, "sffs-rex")
	}
	if err := os.MkdirAll(filepath.Join(c.stateDir, "probes"), 0o700); err != nil {
		return c, err
	}
	c.bytesURL = strings.TrimRight(envOr("SFFS_REX_BYTES_URL", "http://"+c.listen), "/")
	c.token = os.Getenv("SFFS_REX_TOKEN")
	if c.token == "" {
		f := envOr("SFFS_REX_TOKEN_FILE", filepath.Join(c.stateDir, "token"))
		if b, err := os.ReadFile(f); err == nil && len(bytes.TrimSpace(b)) > 0 {
			c.token = string(bytes.TrimSpace(b))
		} else {
			buf := make([]byte, 24)
			if _, err := rand.Read(buf); err != nil {
				return c, err
			}
			c.token = hex.EncodeToString(buf)
			if err := os.WriteFile(f, []byte(c.token+"\n"), 0o600); err != nil {
				return c, err
			}
		}
	}
	return c, nil
}

// ---------------------------------------------------------------- éléments envoyés à rex

type rexIDs struct {
	TMDB int64 `json:"tmdb,omitempty"`
}

type rexItem struct {
	ID        string          `json:"id"`
	Path      string          `json:"path"`
	Kind      string          `json:"kind,omitempty"`
	IDs       *rexIDs         `json:"ids,omitempty"`
	Season    *int            `json:"season,omitempty"`
	Episode   *int            `json:"episode,omitempty"`
	Name      string          `json:"name,omitempty"`
	Release   string          `json:"release,omitempty"`
	Size      uint64          `json:"size,omitempty"`
	Hash      string          `json:"hash,omitempty"`
	Probe     string          `json:"probe,omitempty"`
	MediaInfo json.RawMessage `json:"media_info,omitempty"`
}

// rexFile : un fichier de l'arborescence, avec ce qu'il faut pour en faire un élément rex.
type rexFile struct {
	dir  string // "films/<dossier>" ou "series/<dossier>" (clé des notifications)
	path string // chemin rex : movies/<dossier>/<fichier> ou shows/<dossier>/Season NN/<fichier>
	tmdb int64
	f    *fileNode
}

// rexPath convertit un chemin de l'arborescence sffs en chemin rex (le premier dossier choisit la
// bibliothèque de rex : movies -> Films, shows -> Séries).
func rexPath(top string, parts ...string) string {
	p := map[string]string{"films": "movies", "series": "shows"}[top]
	return p + "/" + strings.Join(parts, "/")
}

// item construit l'élément rex. withInfo : joindre media_info (sinon seule son empreinte part).
func (s *rexSource) item(rf rexFile, withInfo bool) rexItem {
	v := rf.f.v
	info, digest := s.mediaInfo(v)
	var pi probeInfo
	_ = json.Unmarshal(v.probeJSON, &pi)
	it := rexItem{ID: v.id, Path: rf.path, IDs: &rexIDs{TMDB: rf.tmdb}, Name: pi.Release, Release: pi.Release,
		Hash: strings.ToLower(v.infoHash), Probe: digest}
	if rf.tmdb == 0 {
		it.IDs = nil
	}
	if v.sizeExact {
		it.Size = v.size
	}
	if v.mediaType == "series" {
		se, ep := v.season, v.episode
		it.Kind, it.Season, it.Episode = "episode", &se, &ep
	} else {
		it.Kind = "movie"
	}
	if withInfo {
		it.MediaInfo = info
	}
	return it
}

// itemDigest : ce qui, dans un élément, oblige à le renvoyer à rex quand il change.
func itemDigest(it rexItem) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%d|%s|%s", it.Path, it.Probe, it.Size, it.Hash, it.Release)
	if it.Season != nil {
		fmt.Fprintf(h, "|%d|%d", *it.Season, *it.Episode)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// mediaInfo renvoie l'analyse ffprobe de la version (réelle si elle a déjà été lue, sinon
// fabriquée d'après le nom de la release) et son empreinte.
func (s *rexSource) mediaInfo(v *version) (json.RawMessage, string) {
	if b := s.realProbe(v.id); b != nil {
		return b, "ff:" + shortHash(b)
	}
	b := synthMediaInfo(v.probeJSON)
	return b, "sf:" + shortHash(b)
}

func shortHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

var containerFormat = map[string]string{
	"mkv": "matroska,webm", "webm": "matroska,webm", "mp4": "mov,mp4,m4a,3gp,3g2,mj2",
	"avi": "avi", "ts": "mpegts", "m2ts": "mpegts", "wmv": "asf", "mpg": "mpeg", "mpeg": "mpeg",
}

// Langues StreamFusion -> codes ISO 639-2 des étiquettes ffprobe.
var langISO = map[string]string{
	"fr": "fre", "french": "fre", "vff": "fre", "vfq": "fre", "vfi": "fre", "vf": "fre", "vf2": "fre",
	"truefrench": "fre", "en": "eng", "english": "eng", "vo": "eng", "es": "spa", "spanish": "spa",
	"de": "ger", "german": "ger", "it": "ita", "italian": "ita", "ja": "jpn", "japanese": "jpn",
	"pt": "por", "portuguese": "por", "nl": "dut", "ru": "rus", "ko": "kor", "zh": "chi", "ar": "ara",
}

// synthMediaInfo fabrique un résultat au format ffprobe (-show_format -show_streams) à partir du
// probe sffs : une piste vidéo et une piste audio par langue connue (« multi » : français et
// anglais). Pas de durée : rex et Plex l'apprennent à la première lecture (analyse réelle).
func synthMediaInfo(probeJSON []byte) json.RawMessage {
	var pi probeInfo
	_ = json.Unmarshal(probeJSON, &pi)
	format := containerFormat[pi.Container]
	if format == "" {
		format = "matroska,webm"
	}
	video := map[string]any{"index": 0, "codec_type": "video", "codec_name": orDefault(pi.CodecVideo, "h264"),
		"disposition": map[string]int{"default": 1}}
	if pi.Width > 0 {
		video["width"], video["height"] = pi.Width, pi.Height
	}
	pix := "yuv420p"
	if pi.BitDepth >= 10 || pi.HDR {
		pix = "yuv420p10le"
	}
	video["pix_fmt"] = pix
	if pi.HDR {
		video["color_primaries"], video["color_transfer"], video["color_space"] = "bt2020", "smpte2084", "bt2020nc"
	}
	if pi.DolbyVision {
		video["side_data_list"] = []map[string]any{{"side_data_type": "DOVI configuration record",
			"dv_profile": 8, "dv_level": 6, "rpu_present_flag": 1, "bl_present_flag": 1, "dv_bl_signal_compatibility_id": 1}}
	}
	var langs []string
	seen := map[string]bool{}
	for _, l := range pi.Languages {
		codes := []string{langISO[l]}
		if l == "multi" {
			codes = []string{"fre", "eng"}
		}
		for _, c := range codes {
			if c != "" && !seen[c] {
				seen[c] = true
				langs = append(langs, c)
			}
		}
	}
	if len(langs) == 0 {
		langs = []string{"und"}
	}
	streams := []map[string]any{video}
	ch := pi.Channels
	if ch == 0 {
		ch = 2
	}
	for i, l := range langs {
		a := map[string]any{"index": i + 1, "codec_type": "audio", "codec_name": orDefault(pi.CodecAudio, "aac"),
			"channels": ch, "tags": map[string]string{"language": l}, "disposition": map[string]int{"default": b2i(i == 0)}}
		streams = append(streams, a)
	}
	b, _ := json.Marshal(map[string]any{"format": map[string]any{"format_name": format, "nb_streams": len(streams)}, "streams": streams})
	return b
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------- analyses réelles (ffprobe)

func (s *rexSource) probeFile(id string) string {
	return filepath.Join(s.cfg.stateDir, "probes", url.PathEscape(id)+".json")
}

type storedProbe struct {
	Served string          `json:"served"`
	Info   json.RawMessage `json:"media_info"`
}

func (s *rexSource) realProbe(id string) json.RawMessage {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if sp, ok := s.probes[id]; ok {
		return sp.Info
	}
	b, err := os.ReadFile(s.probeFile(id))
	if err != nil {
		return nil
	}
	var sp storedProbe
	if json.Unmarshal(b, &sp) != nil || len(sp.Info) == 0 {
		return nil
	}
	s.probes[id] = sp
	return sp.Info
}

// probeAfterPlay analyse la source servie (une seule fois par source, en tâche de fond) et met
// l'élément à jour dans rex.
func (s *rexSource) probeAfterPlay(rf rexFile) {
	if !s.cfg.probe {
		return
	}
	v := rf.f.v
	served := s.root.st.res.servedHash(v)
	s.pmu.Lock()
	sp, have := s.probes[v.id]
	if (have && sp.Served == served) || s.probing[v.id] {
		s.pmu.Unlock()
		return
	}
	if !have {
		if b, err := os.ReadFile(s.probeFile(v.id)); err == nil && json.Unmarshal(b, &sp) == nil && sp.Served == served {
			s.probes[v.id] = sp
			s.pmu.Unlock()
			return
		}
	}
	s.probing[v.id] = true
	s.pmu.Unlock()
	go func() {
		defer func() {
			s.pmu.Lock()
			delete(s.probing, v.id)
			s.pmu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		u := "http://" + s.addr + "/" + escapePath(rf.path)
		cmd := exec.CommandContext(ctx, s.cfg.ffprobe, "-v", "error", "-print_format", "json",
			"-show_format", "-show_streams", "-show_chapters",
			"-headers", "Authorization: Bearer "+s.cfg.token+"\r\n", u)
		out, err := cmd.Output()
		if err != nil {
			log.Printf("rex : analyse de %s : %v", v.id, err)
			return
		}
		var chk struct {
			Streams []json.RawMessage `json:"streams"`
		}
		if json.Unmarshal(out, &chk) != nil || len(chk.Streams) == 0 {
			log.Printf("rex : analyse de %s : aucune piste", v.id)
			return
		}
		sp := storedProbe{Served: served, Info: compactJSON(out)}
		b, _ := json.Marshal(sp)
		if err := os.WriteFile(s.probeFile(v.id), b, 0o600); err != nil {
			log.Printf("rex : analyse de %s : %v", v.id, err)
		}
		s.pmu.Lock()
		s.probes[v.id] = sp
		s.pmu.Unlock()
		log.Printf("rex : analyse réelle de %s (%d pistes)", v.id, len(chk.Streams))
		s.add([]string{rf.dir})
	}()
}

func compactJSON(b []byte) json.RawMessage {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return b
	}
	return buf.Bytes()
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// ---------------------------------------------------------------- envoi du catalogue

type rexSource struct {
	cfg  rexConfig
	root *rootNode
	http *http.Client
	addr string // adresse réelle du serveur d'octets (ffprobe local)

	mu      sync.Mutex
	pending map[string]bool              // dossiers à renvoyer
	full    bool                         // envoi complet demandé (démarrage, rechargement)
	digest  bool                         // rex accepte les empreintes (features: probe_digest)
	sent    map[string]map[string]string // dossier -> id -> empreinte de l'élément envoyé

	pmu     sync.Mutex
	probes  map[string]storedProbe
	probing map[string]bool
}

var rexFeed *rexSource // nil : source rex coupée

func newRexSource(cfg rexConfig, root *rootNode) *rexSource {
	return &rexSource{cfg: cfg, root: root, http: &http.Client{Timeout: 15 * time.Minute},
		pending: map[string]bool{}, full: true, sent: map[string]map[string]string{},
		probes: map[string]storedProbe{}, probing: map[string]bool{}}
}

// add note des dossiers modifiés ("films/<dossier>", "series/<dossier>").
func (s *rexSource) add(dirs []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	for _, d := range dirs {
		s.pending[d] = true
	}
	s.mu.Unlock()
}

// requestFull demande un envoi complet (après un rechargement de toute la base).
func (s *rexSource) requestFull() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.full = true
	s.mu.Unlock()
}

// start lance le serveur d'octets et la boucle d'envoi.
func (s *rexSource) start() error {
	ln, err := net.Listen("tcp", s.cfg.listen)
	if err != nil {
		return fmt.Errorf("rex : écoute %s : %w", s.cfg.listen, err)
	}
	s.addr = ln.Addr().String()
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	go func() { log.Printf("rex : serveur d'octets arrêté : %v", srv.Serve(ln)) }()
	log.Printf("rex : source %q -> %s, octets servis sur %s", s.cfg.source, s.cfg.url, s.addr)
	go s.loop()
	return nil
}

func (s *rexSource) loop() {
	registered := false
	for {
		if !registered {
			if err := s.register(); err != nil {
				log.Printf("rex : enregistrement de la source : %v (nouvel essai dans une minute)", err)
				time.Sleep(time.Minute)
				continue
			}
			registered = true
		}
		s.mu.Lock()
		full := s.full
		s.full = false
		s.mu.Unlock()
		if full {
			if err := s.snapshot(); err != nil {
				log.Printf("rex : envoi complet : %v (nouvel essai dans une minute)", err)
				s.requestFull()
				registered = false // rex a pu redémarrer et oublier la source
				time.Sleep(time.Minute)
				continue
			}
		}
		if err := s.flush(); err != nil {
			log.Printf("rex : envoi des modifications : %v", err)
		}
		time.Sleep(20 * time.Second)
	}
}

func (s *rexSource) api(method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.cfg.url+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return resp.StatusCode, fmt.Errorf("%s %s : statut %d : %s", method, path, resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s : réponse illisible : %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func (s *rexSource) register() error {
	var r struct {
		Features []string `json:"features"`
	}
	body := map[string]any{"url": s.cfg.bytesURL, "bearer": s.cfg.token, "mount": "/mnt/sffs"}
	if _, err := s.api(http.MethodPut, "/rex/sources/"+url.PathEscape(s.cfg.source), body, &r); err != nil {
		return err
	}
	s.digest = false
	for _, f := range r.Features {
		if f == "probe_digest" {
			s.digest = true
		}
	}
	log.Printf("rex : source %q enregistrée (empreintes : %v)", s.cfg.source, s.digest)
	return nil
}

// files liste les fichiers de l'arborescence (dirs vide : tous), groupés par dossier.
func (s *rexSource) files(dirs map[string]bool) map[string][]rexFile {
	n := s.root
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string][]rexFile{}
	walk := func(top string, base *fs.Inode, ids map[int64]string, series bool) {
		if base == nil {
			return
		}
		tmdbOf := make(map[string]int64, len(ids))
		for id, d := range ids {
			tmdbOf[d] = id
		}
		for dname, d := range base.Children() {
			key := top + "/" + dname
			if dirs != nil && !dirs[key] {
				continue
			}
			add := func(sub *fs.Inode, parts ...string) {
				for fname, ch := range sub.Children() {
					if f, ok := ch.Operations().(*fileNode); ok {
						p := append(append([]string{}, parts...), fname)
						out[key] = append(out[key], rexFile{dir: key, path: rexPath(top, p...), tmdb: tmdbOf[dname], f: f})
					}
				}
			}
			if series {
				for sname, sd := range d.Children() {
					add(sd, dname, sname)
				}
			} else {
				add(d, dname)
			}
			if _, ok := out[key]; !ok {
				out[key] = nil // dossier vide ou retiré : ses anciens fichiers partent
			}
		}
	}
	walk("films", n.films, n.movieDirs, false)
	walk("series", n.series, n.showDirs, true)
	return out
}

const rexChunk = 300

type rexUpsertResult struct {
	Created  int               `json:"created"`
	Updated  int               `json:"updated"`
	Held     int               `json:"held"`
	Rejected []json.RawMessage `json:"rejected"`
	Wants    []string          `json:"wants"`
	Removed  json.RawMessage   `json:"removed"`
}

// snapshot envoie tout le catalogue en une session : rex retire ensuite ce qui n'y est plus.
func (s *rexSource) snapshot() error {
	t0 := time.Now()
	byDir := s.files(nil)
	var all []rexFile
	for _, lst := range byDir {
		all = append(all, lst...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].path < all[j].path })
	if len(all) == 0 {
		return errors.New("arborescence vide, rien envoyé")
	}
	session := time.Now().UTC().Format(time.RFC3339Nano)
	byID := map[string]rexFile{}
	sent := map[string]map[string]string{}
	var created, updated, wanted, rejected int
	for part, i := 0, 0; i < len(all); part, i = part+1, i+rexChunk {
		end := min(i+rexChunk, len(all))
		items := make([]rexItem, 0, end-i)
		for _, rf := range all[i:end] {
			it := s.item(rf, !s.digest)
			items = append(items, it)
			byID[rf.f.v.id] = rf
			if sent[rf.dir] == nil {
				sent[rf.dir] = map[string]string{}
			}
			sent[rf.dir][it.ID] = itemDigest(it)
		}
		body := map[string]any{"session": session, "part": part, "complete": end == len(all), "items": items}
		if end == len(all) && os.Getenv("SFFS_REX_FORCE") == "1" {
			body["force"] = true
		}
		var r rexUpsertResult
		if _, err := s.api(http.MethodPut, "/rex/feed/"+url.PathEscape(s.cfg.source)+"/snapshot", body, &r); err != nil {
			return err
		}
		created, updated, rejected = created+r.Created, updated+r.Updated, rejected+len(r.Rejected)
		if len(r.Wants) > 0 {
			wanted += len(r.Wants)
			if err := s.sendWanted(r.Wants, byID); err != nil {
				return err
			}
		}
		if part%20 == 19 {
			log.Printf("rex : envoi complet en cours, %d/%d fichiers", end, len(all))
		}
	}
	for d := range byDir {
		if sent[d] == nil {
			sent[d] = map[string]string{}
		}
	}
	s.mu.Lock()
	s.sent = sent
	s.mu.Unlock()
	log.Printf("rex : envoi complet : %d fichiers (%d créés, %d mis à jour, %d analyses demandées, %d refusés) en %s",
		len(all), created, updated, wanted, rejected, time.Since(t0).Round(time.Second))
	return nil
}

// sendWanted renvoie, avec leur media_info, les éléments dont rex ne connaît pas l'empreinte.
func (s *rexSource) sendWanted(ids []string, byID map[string]rexFile) error {
	for i := 0; i < len(ids); i += rexChunk {
		var items []rexItem
		for _, id := range ids[i:min(i+rexChunk, len(ids))] {
			if rf, ok := byID[id]; ok {
				items = append(items, s.item(rf, true))
			}
		}
		if len(items) == 0 {
			continue
		}
		var r rexUpsertResult
		if _, err := s.api(http.MethodPost, "/rex/feed/"+url.PathEscape(s.cfg.source)+"/items", map[string]any{"items": items}, &r); err != nil {
			return err
		}
	}
	return nil
}

// flush envoie les dossiers modifiés depuis le dernier passage : nouveaux fichiers et fichiers
// changés (avec media_info), retrait des fichiers disparus.
func (s *rexSource) flush() error {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return nil
	}
	dirs := s.pending
	s.pending = map[string]bool{}
	s.mu.Unlock()

	byDir := s.files(dirs)
	var upserts []rexItem
	var gone []string
	next := map[string]map[string]string{}
	s.mu.Lock()
	for d := range dirs {
		old := s.sent[d]
		cur := map[string]string{}
		for _, rf := range byDir[d] {
			it := s.item(rf, true)
			dg := itemDigest(it)
			cur[it.ID] = dg
			if old[it.ID] != dg {
				upserts = append(upserts, it)
			}
		}
		for id := range old {
			if _, ok := cur[id]; !ok {
				gone = append(gone, id)
			}
		}
		next[d] = cur
	}
	s.mu.Unlock()

	retry := func() {
		s.add(keys(dirs))
	}
	src := "/rex/feed/" + url.PathEscape(s.cfg.source)
	for i := 0; i < len(upserts); i += rexChunk {
		var r rexUpsertResult
		if _, err := s.api(http.MethodPost, src+"/items", map[string]any{"items": upserts[i:min(i+rexChunk, len(upserts))]}, &r); err != nil {
			retry()
			return err
		}
	}
	for i := 0; i < len(gone); i += 1000 {
		if _, err := s.api(http.MethodDelete, src+"/items", map[string]any{"ids": gone[i:min(i+1000, len(gone))]}, nil); err != nil {
			retry()
			return err
		}
	}
	s.mu.Lock()
	for d, cur := range next {
		if len(cur) == 0 {
			delete(s.sent, d)
		} else {
			s.sent[d] = cur
		}
	}
	s.mu.Unlock()
	if len(upserts)+len(gone) > 0 {
		log.Printf("rex : %d fichiers envoyés, %d retirés (%d dossiers)", len(upserts), len(gone), len(dirs))
	}
	return nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------- serveur d'octets

// lookup retrouve le fichier d'un chemin rex (movies/… ou shows/…).
func (s *rexSource) lookup(p string) (rexFile, bool) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 3 {
		return rexFile{}, false
	}
	n := s.root
	var top string
	var cur *fs.Inode
	switch parts[0] {
	case "movies":
		top, cur = "films", n.films
	case "shows":
		top, cur = "series", n.series
	default:
		return rexFile{}, false
	}
	if cur == nil || (top == "films" && len(parts) != 3) || (top == "series" && len(parts) != 4) {
		return rexFile{}, false
	}
	for _, seg := range parts[1:] {
		if cur = cur.GetChild(seg); cur == nil {
			return rexFile{}, false
		}
	}
	f, ok := cur.Operations().(*fileNode)
	if !ok {
		return rexFile{}, false
	}
	return rexFile{dir: top + "/" + parts[1], path: rexPath(top, parts[1:]...), f: f}, true
}

func (s *rexSource) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.cfg.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rf, ok := s.lookup(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	v := rf.f.v
	ctx := r.Context()
	fromFFprobe := strings.HasPrefix(r.UserAgent(), "Lavf")
	if !fromFFprobe {
		log.Printf("rex : lecture %s %q (%s)", v.id, v.name, orDefault(r.Header.Get("Range"), "tout"))
	}
	if !v.sizeExact {
		rf.f.learnSize(ctx) // résout aussi le lien (et la remplaçante si hors cache)
		if v.sizeExact {
			s.add([]string{rf.dir})
		}
	}
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		u, err := s.root.st.res.resolve(ctx, v, attempt > 0)
		if errors.Is(err, errNotCached) {
			log.Printf("rex : %s hors cache, aucune version de remplacement", v.id)
			http.Error(w, "not cached", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			log.Printf("rex : résolution de %s : %v", v.id, err)
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if rg := r.Header.Get("Range"); rg != "" {
			req.Header.Set("Range", rg)
		} else if r.Method == http.MethodHead {
			req.Header.Set("Range", "bytes=0-0")
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err = s.root.st.data.Do(req)
		if err == nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent ||
			resp.StatusCode == http.StatusRequestedRangeNotSatisfiable) {
			break
		}
		if err == nil {
			log.Printf("rex : flux %s : statut %d%s", v.id, resp.StatusCode, map[bool]string{true: ", renégociation", false: ""}[attempt == 0])
			resp.Body.Close()
			resp = nil
		} else if attempt == 1 {
			log.Printf("rex : flux %s : %v", v.id, err)
		}
	}
	if resp == nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	h := w.Header()
	for _, k := range []string{"Content-Range", "Content-Length", "Last-Modified", "ETag"} {
		if val := resp.Header.Get(k); val != "" {
			h.Set(k, val)
		}
	}
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Type", map[bool]string{true: "video/mp4", false: "video/x-matroska"}[strings.HasSuffix(v.name, ".mp4")])
	if r.Method == http.MethodHead {
		h.Del("Content-Range")
		h.Set("Content-Length", fmt.Sprint(v.size))
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(resp.StatusCode)
	if !fromFFprobe && resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		s.probeAfterPlay(rf) // en tâche de fond, une fois par source servie
	}
	// Un lecteur coupe la connexion à chaque saut dans le fichier : seule une erreur côté
	// débrideur mérite d'être signalée.
	if _, err := io.Copy(w, resp.Body); err != nil && ctx.Err() == nil &&
		!errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
		log.Printf("rex : flux %s interrompu : %v", v.id, err)
	}
}
