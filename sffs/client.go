package main

// Application qui lit un fichier. Sur le système, tous les fichiers sont ouverts par le même
// processus (silo) : l'appareil et l'application du spectateur ne sont connus que de Silo. Au
// lancement d'une lecture, sffs lit les sessions en cours de Silo (/api/v2/admin/sessions, clé
// d'API admin) et retient celle qui correspond au titre lu et à l'heure du lancement.

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

type siloSession struct {
	SessionID        string    `json:"session_id"`
	Username         string    `json:"username"`
	ProfileName      string    `json:"profile_name"`
	MediaTitle       string    `json:"media_title"`
	MediaType        string    `json:"media_type"`
	SeriesName       string    `json:"series_name"`
	SeasonNumber     *int      `json:"season_number"`
	EpisodeNumber    *int      `json:"episode_number"`
	PlayMethod       string    `json:"play_method"`
	StartedAt        time.Time `json:"started_at"`
	ClientName       string    `json:"client_name"`
	ClientVersion    string    `json:"client_version"`
	ClientLabel      string    `json:"client_label"`
	TargetResolution string    `json:"target_resolution"`
}

// playClient : qui lit, d'après Silo.
type playClient struct {
	App        string `json:"app,omitempty"`  // ex. « Infuse 8.1 », « Silo Web »
	User       string `json:"user,omitempty"` // utilisateur (et profil)
	PlayMethod string `json:"play_method,omitempty"`
	Resolution string `json:"resolution,omitempty"` // résolution du transcodage
}

// fold : minuscules, sans accents ni ponctuation, espaces simples.
func fold(s string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		default:
			space = true
		}
	}
	return b.String()
}

// sessionMatches : la session de Silo lit-elle cette version ? dir : dossier du titre chez sffs.
func sessionMatches(s siloSession, v *version, dir string, armed time.Time) bool {
	if !s.StartedAt.IsZero() && (s.StartedAt.Before(armed.Add(-2*time.Minute)) || s.StartedAt.After(armed.Add(2*time.Minute))) {
		return false
	}
	d := fold(dir)
	if v.mediaType == "series" {
		if s.SeasonNumber == nil || s.EpisodeNumber == nil || *s.SeasonNumber != v.season || *s.EpisodeNumber != v.episode {
			return false
		}
		name := fold(s.SeriesName)
		return name != "" && strings.Contains(d, name)
	}
	if s.MediaType != "" && s.MediaType != "movie" {
		return false
	}
	t := fold(s.MediaTitle)
	return t != "" && strings.Contains(d, t)
}

func clientOf(s siloSession) playClient {
	c := playClient{App: s.ClientLabel, User: s.Username, PlayMethod: s.PlayMethod, Resolution: s.TargetResolution}
	if c.App == "" {
		c.App = strings.TrimSpace(s.ClientName + " " + s.ClientVersion)
	}
	if s.ProfileName != "" && s.ProfileName != s.Username {
		c.User += " (" + s.ProfileName + ")"
	}
	return c
}

// identifyClient cherche, quelques secondes après l'armement, la session Silo de cette lecture.
// Silo crée sa session pendant le démarrage de la lecture : plusieurs essais.
func (s *siloNotifier) identifyClient(id string, v *version, dir string, armed time.Time) {
	if s == nil || s.wc.siloURL == "" {
		return
	}
	for _, wait := range []time.Duration{3 * time.Second, 7 * time.Second, 20 * time.Second, 30 * time.Second} {
		time.Sleep(wait)
		if s.key() == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		var out struct {
			Items []siloSession `json:"items"`
		}
		err := s.api(ctx, http.MethodGet, "/api/v2/admin/sessions", nil, &out)
		cancel()
		if err != nil {
			log.Printf("silo : sessions illisibles (%v)", err)
			return
		}
		for _, ss := range out.Items {
			if sessionMatches(ss, v, dir, armed) {
				c := clientOf(ss)
				stats.playClient(id, c)
				log.Printf("lecture %s par %s (%s, %s)", id, c.App, c.User, c.PlayMethod)
				return
			}
		}
	}
}
