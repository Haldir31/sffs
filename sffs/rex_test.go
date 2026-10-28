package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSynthMediaInfo(t *testing.T) {
	pj, _ := json.Marshal(probeInfo{Container: "mkv", Width: 3840, Height: 2160, CodecVideo: "hevc", CodecAudio: "truehd",
		Channels: 8, HDR: true, DolbyVision: true, BitDepth: 10, Languages: []string{"multi", "fr"}})
	var mi struct {
		Format  map[string]any   `json:"format"`
		Streams []map[string]any `json:"streams"`
	}
	if err := json.Unmarshal(synthMediaInfo(pj), &mi); err != nil {
		t.Fatal(err)
	}
	if mi.Format["format_name"] != "matroska,webm" {
		t.Errorf("format_name = %v", mi.Format["format_name"])
	}
	if _, ok := mi.Format["duration"]; ok {
		t.Error("durée inventée")
	}
	if len(mi.Streams) != 3 { // vidéo + fre + eng (multi), fr déjà vu
		t.Fatalf("%d pistes, attendu 3", len(mi.Streams))
	}
	v := mi.Streams[0]
	if v["codec_name"] != "hevc" || v["width"].(float64) != 3840 || v["color_transfer"] != "smpte2084" || v["pix_fmt"] != "yuv420p10le" {
		t.Errorf("piste vidéo : %v", v)
	}
	if v["side_data_list"] == nil {
		t.Error("Dolby Vision absent")
	}
	langs := []string{}
	for _, a := range mi.Streams[1:] {
		if a["codec_name"] != "truehd" || a["channels"].(float64) != 8 {
			t.Errorf("piste audio : %v", a)
		}
		langs = append(langs, a["tags"].(map[string]any)["language"].(string))
	}
	if langs[0] != "fre" || langs[1] != "eng" {
		t.Errorf("langues %v", langs)
	}
}

func TestSynthMediaInfoMinimal(t *testing.T) {
	var mi struct {
		Streams []map[string]any `json:"streams"`
	}
	if err := json.Unmarshal(synthMediaInfo([]byte(`{}`)), &mi); err != nil {
		t.Fatal(err)
	}
	if len(mi.Streams) != 2 || mi.Streams[1]["tags"].(map[string]any)["language"] != "und" {
		t.Errorf("pistes %v", mi.Streams)
	}
}

func TestRexPath(t *testing.T) {
	if p := rexPath("films", "Alien (1979) {tmdb-348}", "Alien.mkv"); p != "movies/Alien (1979) {tmdb-348}/Alien.mkv" {
		t.Error(p)
	}
	if p := rexPath("series", "Lupin (2021) {tmdb-96677}", "Season 01", "x.mkv"); p != "shows/Lupin (2021) {tmdb-96677}/Season 01/x.mkv" {
		t.Error(p)
	}
	if e := escapePath("shows/A b/Season 01/c#d.mkv"); e != "shows/A%20b/Season%2001/c%23d.mkv" {
		t.Error(e)
	}
}

func TestItemDigest(t *testing.T) {
	se, ep := 1, 2
	a := rexItem{ID: "x", Path: "shows/a/Season 01/b.mkv", Probe: "sf:1", Season: &se, Episode: &ep}
	b := a
	if itemDigest(a) != itemDigest(b) {
		t.Error("empreinte instable")
	}
	b.Size = 42
	if itemDigest(a) == itemDigest(b) {
		t.Error("la taille ne change pas l'empreinte")
	}
	c := a
	c.Probe = "ff:2"
	if itemDigest(a) == itemDigest(c) {
		t.Error("l'analyse ne change pas l'empreinte")
	}
}

func TestRexRegister(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/rex/sources/sffs" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"name":"sffs","features":["probe_digest"]}`))
	}))
	defer srv.Close()
	s := newRexSource(rexConfig{url: srv.URL, source: "sffs", bytesURL: "http://127.0.0.1:1", token: "t"}, nil)
	if err := s.register(); err != nil {
		t.Fatal(err)
	}
	if !s.digest || got["bearer"] != "t" || got["url"] != "http://127.0.0.1:1" {
		t.Errorf("digest=%v corps=%v", s.digest, got)
	}
}

func TestRexServeAuth(t *testing.T) {
	s := newRexSource(rexConfig{token: "secret"}, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/movies/a/b.mkv", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("sans jeton : %d", rec.Code)
	}
}

func TestCompatOrder(t *testing.T) {
	uhdRemux := probeInfo{Resolution: "2160p", CodecVideo: "hevc", CodecAudio: "dts", HDR: true}
	fhdX264 := probeInfo{Resolution: "1080p", CodecVideo: "h264", CodecAudio: "ac3"}
	fhdHevcDts := probeInfo{Resolution: "1080p", CodecVideo: "hevc", CodecAudio: "dts"}
	hd := probeInfo{Resolution: "720p", CodecVideo: "h264", CodecAudio: "aac"}
	order := []probeInfo{fhdX264, fhdHevcDts, hd, uhdRemux}
	for i := 0; i+1 < len(order); i++ {
		if lessRank(compatRank(order[i]), compatRank(order[i+1])) >= 0 {
			t.Errorf("compat : %v devrait passer avant %v", order[i], order[i+1])
		}
	}
	if lessRank(qualityRank(uhdRemux, true), qualityRank(fhdX264, false)) >= 0 {
		t.Error("quality : la 4K REMUX devrait passer en tête")
	}
}
