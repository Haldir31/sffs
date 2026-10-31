package main

import (
	"testing"
	"time"
)

func withCommunity(t *testing.T, m map[string]int64) {
	t.Helper()
	old := community
	community = &communityCache{m: m, dropped: map[string]bool{}, maxAge: 7 * 24 * time.Hour, enabled: true}
	t.Cleanup(func() { community = old })
}

func TestCommunityHasAgeAndDrop(t *testing.T) {
	now := time.Now().Unix()
	withCommunity(t, map[string]int64{"aa": now, "old": now - 8*24*3600})
	if !community.has("AA") {
		t.Fatal("entrée récente (casse ignorée) attendue")
	}
	if community.has("old") || community.has("absent") {
		t.Fatal("entrée trop ancienne ou absente : pas en cache")
	}
	community.drop("aa")
	if community.has("aa") {
		t.Fatal("entrée démentie par une lecture : à oublier")
	}
	community.enabled = false
	if community.has("aa") {
		t.Fatal("SFFS_COMMUNITY_CACHE=0 : ignoré")
	}
}

// StreamFusion garde le dernier mot sur ce qu'il a vérifié ; la communauté ne comble que les trous.
func TestCommunityFallbackOnlyForUnverified(t *testing.T) {
	now := time.Now().Unix()
	withCommunity(t, map[string]int64{"pack": now, "listed": now})
	e := &cacheEntry{srcs: []streamSrc{{hash: "listed", instant: false}}}
	if !communityFallback(e, "pack") {
		t.Fatal("absent de la liste StreamFusion et vu en cache : à jouer")
	}
	if communityFallback(e, "listed") {
		t.Fatal("vérifié hors cache par StreamFusion : son verdict prime")
	}
	if communityFallback(e, "inconnu") {
		t.Fatal("ni vérifié ni connu : pas en cache")
	}
}

func TestCommunityPrioritizesSelection(t *testing.T) {
	withCommunity(t, map[string]int64{"c": time.Now().Unix()})
	vs := []*version{
		{id: "a", infoHash: "a", bucket: "1080", releaseKey: "a", seeders: 100},
		{id: "c", infoHash: "c", bucket: "1080", releaseKey: "c", seeders: 1},
	}
	for _, v := range vs {
		if community.has(v.infoHash) {
			v.available = true
		}
	}
	got := selectVersions(vs, config{langs: []string{"fr"}}, 0, 1)
	if len(got) != 1 || got[0].id != "c" {
		t.Fatalf("la version en cache communautaire doit passer devant malgré moins de seeders : %v", got[0].id)
	}
}
