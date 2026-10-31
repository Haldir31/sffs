package main

// Cache communautaire : la table debrid_cache de StreamFusion liste les torrents vus en cache
// chez AllDebrid, par cette installation ou par la communauté (StreamFusion ≥ 1.13.13 la
// synchronise). sffs s'en sert en complément de ses vérifications, jamais à leur place :
//
//   - au chargement, une version connue en cache passe en tête de la sélection (comme les
//     versions déjà vues en cache par sffs) ;
//   - au lancement, StreamFusion est toujours interrogé. Une version qu'il vient de vérifier
//     garde son verdict ; une version absente de sa liste (au-delà de ses meilleurs résultats,
//     cas des packs de saison) est jouée si la communauté l'a vue en cache récemment.
//
// Une entrée démentie à la lecture (vidéo d'attente de StreamFusion) est oubliée.

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const communityRefresh = 15 * time.Minute // rythme de la synchronisation communautaire de StreamFusion

type communityCache struct {
	mu      sync.RWMutex
	m       map[string]int64 // info_hash -> vérifié en cache (unix)
	dropped map[string]bool  // démentis par une lecture : ignorés jusqu'au redémarrage
	maxAge  time.Duration
	enabled bool
	loaded  time.Time
	failed  bool
}

var community = &communityCache{m: map[string]int64{}, dropped: map[string]bool{}}

func configureCommunity() {
	community.enabled = os.Getenv("SFFS_COMMUNITY_CACHE") != "0"
	community.maxAge = 7 * 24 * time.Hour
	if v := strings.TrimSpace(os.Getenv("SFFS_COMMUNITY_MAX_AGE")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Fatalf("SFFS_COMMUNITY_MAX_AGE: %q invalide", v)
		}
		community.maxAge = d
	}
}

// refresh recharge les torrents vus en cache récemment. En cas d'erreur (droits de lecture,
// StreamFusion trop ancien sans la table), la liste précédente est gardée.
func (c *communityCache) refresh(ctx context.Context, dsn string) {
	if !c.enabled {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		c.fail(err)
		return
	}
	defer conn.Close(context.Background())
	since := time.Now().Add(-c.maxAge).Unix()
	rows, err := conn.Query(ctx, `
		select lower(btrim(info_hash)), checked_at
		  from debrid_cache
		 where service = 'alldebrid' and cached_data is not null
		   and checked_at >= $1 and expires_at > extract(epoch from now())`, since)
	if err != nil {
		c.fail(err)
		return
	}
	m := map[string]int64{}
	for rows.Next() {
		var h string
		var at int64
		if rows.Scan(&h, &at) == nil && h != "" {
			m[h] = at
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		c.fail(err)
		return
	}
	c.mu.Lock()
	first := c.loaded.IsZero()
	c.m, c.loaded, c.failed = m, time.Now(), false
	c.mu.Unlock()
	if first {
		log.Printf("cache communautaire : %d torrents vus en cache depuis moins de %s", len(m), c.maxAge)
	}
}

func (c *communityCache) fail(err error) {
	c.mu.Lock()
	already := c.failed
	c.failed = true
	c.mu.Unlock()
	if !already {
		log.Printf("cache communautaire indisponible (debrid_cache) : %v", err)
	}
}

func (c *communityCache) loop(ctx context.Context, dsn string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(communityRefresh):
			c.refresh(ctx, dsn)
		}
	}
}

// has indique si le torrent a été vu en cache récemment (et n'a pas été démenti depuis).
func (c *communityCache) has(hash string) bool {
	if c == nil || !c.enabled {
		return false
	}
	hash = strings.ToLower(hash)
	c.mu.RLock()
	defer c.mu.RUnlock()
	at, ok := c.m[hash]
	return ok && !c.dropped[hash] && time.Since(time.Unix(at, 0)) < c.maxAge
}

// drop oublie un torrent démenti par une lecture.
func (c *communityCache) drop(hash string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.dropped[strings.ToLower(hash)] = true
	c.mu.Unlock()
}

// inList indique si StreamFusion a renvoyé ce torrent dans la liste de sources (donc vérifié).
func (e *cacheEntry) inList(hash string) bool {
	for i := range e.srcs {
		if e.srcs[i].hash == hash {
			return true
		}
	}
	return false
}

// communityFallback : StreamFusion n'a pas vérifié ce torrent (absent de sa liste) mais la
// communauté l'a vu en cache récemment.
func communityFallback(e *cacheEntry, hash string) bool {
	return !e.inList(hash) && community.has(hash)
}
