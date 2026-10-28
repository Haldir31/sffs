package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Création des bibliothèques sffs dans Silo (SFFS_SILO_LIBRARIES=1) : dès que la clé d'API Silo
// existe (créée après le compte admin), une bibliothèque de films et une de séries sont créées si
// aucune bibliothèque ne contient déjà ces dossiers. Une seule fois : le fichier repère
// (SFFS_SILO_LIBRARIES_DONE) évite de recréer une bibliothèque supprimée volontairement.
//
//	SFFS_SILO_LIBRARIES_LANG          langue des métadonnées (défaut fr)
//	SFFS_SILO_FILMS_NAME / _SERIES_NAME  noms des bibliothèques (défaut Films, Séries)

type siloLibrary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Paths       []string `json:"paths"`
	ScanWarning *string  `json:"scan_warning_code"`
}

type libraryWanted struct {
	path, kind, name string
}

func (s *siloNotifier) libraryLoop(series bool) {
	if os.Getenv("SFFS_SILO_LIBRARIES") != "1" || s == nil || s.wc.siloURL == "" {
		return
	}
	done := os.Getenv("SFFS_SILO_LIBRARIES_DONE")
	if done != "" {
		if _, err := os.Stat(done); err == nil {
			return
		}
	}
	want := []libraryWanted{{s.wc.siloFilms, "movies", envOr("SFFS_SILO_FILMS_NAME", "Films")}}
	if series {
		want = append(want, libraryWanted{s.wc.siloSeries, "series", envOr("SFFS_SILO_SERIES_NAME", "Séries")})
	}
	lang := envOr("SFFS_SILO_LIBRARIES_LANG", "fr")
	for {
		if s.key() != "" {
			err := s.ensureLibraries(want, lang)
			if err == nil {
				if done != "" {
					_ = os.WriteFile(done, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
				}
				return
			}
			log.Printf("silo : bibliothèques non créées, nouvel essai dans une minute (%v)", err)
		}
		time.Sleep(time.Minute)
	}
}

func (s *siloNotifier) ensureLibraries(want []libraryWanted, lang string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	libs, err := s.libraries(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, l := range libs {
		for _, p := range l.Paths {
			have[p] = true
		}
	}
	for _, w := range want {
		if have[w.path] {
			continue
		}
		// Pas de surveillance en temps réel : sffs prévient Silo des nouveautés lui-même, et sur
		// 160 000 fichiers la file d'événements du noyau déborde et déclenche des rescans complets.
		body := map[string]any{"paths": []string{w.path}, "type": w.kind, "name": w.name,
			"metadata_language": lang, "realtime_monitoring": false}
		var created struct {
			ID string `json:"id"`
		}
		if err := s.api(ctx, http.MethodPost, "/api/v2/libraries", body, &created); err != nil {
			return fmt.Errorf("création de %s : %w", w.name, err)
		}
		log.Printf("silo : bibliothèque %q créée (%s)", w.name, w.path)
		if err := s.disableNFO(ctx, created.ID); err != nil {
			log.Printf("silo : bibliothèque %q : fournisseur NFO non désactivé : %v", w.name, err)
		}
	}
	return nil
}

// disableNFO retire le fournisseur de métadonnées NFO de la chaîne de la bibliothèque : il n'y a
// jamais de fichier .nfo à côté des fichiers sffs, et chaque recherche échouée encombre les journaux.
func (s *siloNotifier) disableNFO(ctx context.Context, id string) error {
	path := "/api/v2/libraries/" + id + "/providers"
	type entry struct {
		PluginInstallationID string `json:"plugin_installation_id"`
		CapabilityID         string `json:"capability_id"`
		Priority             int    `json:"priority"`
		Enabled              bool   `json:"enabled"`
	}
	type level struct {
		ContentLevel string  `json:"content_level"`
		Entries      []entry `json:"entries"`
	}
	var chain struct {
		Levels []level `json:"levels"`
	}
	if err := s.api(ctx, http.MethodGet, path, nil, &chain); err != nil {
		return err
	}
	for i := range chain.Levels {
		for j := range chain.Levels[i].Entries {
			if chain.Levels[i].Entries[j].CapabilityID == "nfo" {
				chain.Levels[i].Entries[j].Enabled = false
			}
		}
	}
	return s.api(ctx, http.MethodPut, path, chain, nil)
}

// libraries liste les bibliothèques de Silo (API v2, toutes les pages).
func (s *siloNotifier) libraries(ctx context.Context) ([]siloLibrary, error) {
	var all []siloLibrary
	path := "/api/v2/libraries"
	for range 100 {
		var page struct {
			Items []siloLibrary `json:"items"`
			Page  struct {
				HasMore    bool   `json:"has_more"`
				NextCursor string `json:"next_cursor"`
			} `json:"page"`
		}
		if err := s.api(ctx, http.MethodGet, path, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.Page.HasMore || page.Page.NextCursor == "" {
			break
		}
		path = "/api/v2/libraries?cursor=" + url.QueryEscape(page.Page.NextCursor)
	}
	return all, nil
}

func (s *siloNotifier) api(ctx context.Context, method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.wc.siloURL+path, &body)
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
	if resp.StatusCode/100 != 2 {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return fmt.Errorf("%s %s : statut %d %.200s", method, path, resp.StatusCode, b.String())
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// healLoop relance le scan d'une bibliothèque sffs que Silo a marquée injoignable (dead_root),
// une fois son dossier de nouveau lisible. Cas typique : sffs redémarré pendant un scan, Silo a vu
// les dossiers disparaître une minute ; il ne supprime rien mais ne rescanne pas de lui-même.
func (s *siloNotifier) healLoop(every time.Duration) {
	if s == nil || s.wc.siloURL == "" {
		return
	}
	roots := map[string]bool{s.wc.siloFilms: true, s.wc.siloSeries: true}
	for range time.Tick(every) {
		if s.key() == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		libs, err := s.libraries(ctx)
		if err != nil {
			cancel()
			continue
		}
		for _, l := range libs {
			if l.ScanWarning == nil || *l.ScanWarning != "dead_root" {
				continue
			}
			for _, p := range l.Paths {
				if !roots[p] {
					continue
				}
				if _, err := os.Stat(p); err != nil {
					continue // pas encore lisible côté sffs : prochain passage
				}
				if err := s.api(ctx, http.MethodPost, "/api/v2/scan", map[string]string{"path": p}, nil); err != nil {
					log.Printf("silo : bibliothèque %q injoignable, relance du scan impossible : %v", l.Name, err)
				} else {
					log.Printf("silo : bibliothèque %q marquée injoignable, scan relancé (%s)", l.Name, p)
				}
			}
		}
		cancel()
	}
}
