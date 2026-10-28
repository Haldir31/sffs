package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestHealLoopRescansDeadRoot(t *testing.T) {
	films, series := t.TempDir(), t.TempDir()
	dead := "dead_root"
	var mu sync.Mutex
	var scanned []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/libraries":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []siloLibrary{
				{ID: "1", Name: "Films", Paths: []string{films}, ScanWarning: &dead},
				{ID: "2", Name: "Séries", Paths: []string{series}},
				{ID: "3", Name: "Autre", Paths: []string{"/ailleurs"}, ScanWarning: &dead},
			}, "page": map[string]any{"has_more": false}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/scan":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			scanned = append(scanned, body["path"])
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	keyFile := t.TempDir() + "/key"
	_ = os.WriteFile(keyFile, []byte("sa_test\n"), 0o600)
	s := newSiloNotifier(watchConfig{siloURL: srv.URL, siloKeyFile: keyFile, siloFilms: films, siloSeries: series})
	go s.healLoop(20 * time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(scanned) == 0 {
		t.Fatal("aucun scan relancé pour la bibliothèque injoignable")
	}
	for _, p := range scanned {
		if p != films {
			t.Fatalf("scan relancé pour %q, attendu seulement %q", p, films)
		}
	}
}

func TestEnsureLibrariesV2(t *testing.T) {
	var mu sync.Mutex
	var created []map[string]any
	var chainPut map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/libraries":
			if r.URL.Query().Get("cursor") == "" {
				_, _ = w.Write([]byte(`{"items":[{"id":"1","paths":["/autre"]}],"page":{"has_more":true,"next_cursor":"c2"}}`))
			} else {
				_, _ = w.Write([]byte(`{"items":[],"page":{"has_more":false}}`))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/libraries":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			created = append(created, b)
			_, _ = w.Write([]byte(`{"id":"7"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/libraries/7/providers":
			_, _ = w.Write([]byte(`{"levels":[{"content_level":"movie","entries":[{"plugin_installation_id":"1","capability_id":"nfo","provider_slug":"nfo","priority":0,"enabled":true,"local":true},{"plugin_installation_id":"2","capability_id":"tmdb","provider_slug":"tmdb","priority":1,"enabled":true,"local":false}]}]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2/libraries/7/providers":
			_ = json.NewDecoder(r.Body).Decode(&chainPut)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := newSiloNotifier(watchConfig{siloURL: srv.URL, siloKey: "sa_test"})
	if err := s.ensureLibraries([]libraryWanted{{"/mnt/sffs/films", "movies", "Films"}}, "fr"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 || created[0]["realtime_monitoring"] != false || created[0]["metadata_language"] != "fr" {
		t.Fatalf("création : %v", created)
	}
	lvl := chainPut["levels"].([]any)[0].(map[string]any)
	if lvl["content_level"] != "movie" {
		t.Fatalf("niveau envoyé : %v", lvl)
	}
	levels := lvl["entries"].([]any)
	for _, e := range levels {
		if _, ok := e.(map[string]any)["provider_slug"]; ok {
			t.Fatalf("champ en lecture seule renvoyé : %v", e)
		}
		m := e.(map[string]any)
		if want := m["capability_id"] != "nfo"; m["enabled"] != want {
			t.Fatalf("chaîne envoyée : %v", levels)
		}
	}
}
