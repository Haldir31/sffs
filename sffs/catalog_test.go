package main

import (
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
)

func TestCatalog(t *testing.T) {
	cfg := config{series: true}
	mk := func(id, name, bucket string, season, episode int) *version {
		return &version{id: id, name: name, bucket: bucket, size: 1 << 30, sizeExact: true, infoHash: id + "aaaaaaaa",
			mediaType: map[bool]string{true: "series", false: "movie"}[season > 0], season: season, episode: episode,
			probeJSON: []byte(`{"container":"mkv","codec_video":"hevc","hdr":true}`)}
	}
	root := &rootNode{
		st:     &state{cfg: cfg, res: newResolver(cfg)},
		movies: []*movie{{tmdb: 1, dirName: "L'Été meurtrier (1983) {tmdb-1}", versions: []*version{mk("m1", "a.mkv", "1080", 0, 0), mk("m2", "b.mkv", "2160", 0, 0)}}},
		shows: []*show{{tmdb: 2, dirName: "Gilmore Girls (2000) {tmdb-2}", seasons: map[int][]*version{
			0: {mk("s0", "Gilmore Girls - S00E01 - x.mkv", "1080", 0, 1)},
			1: {mk("s1", "Gilmore Girls - S01E02 - x.mkv", "1080", 1, 2), mk("s2", "Gilmore Girls - S01E01 - y.mkv", "720", 1, 1)},
		}}},
	}
	fs.NewNodeFS(root, &fs.Options{})

	hits, ok := root.searchCatalog("ete meurtrier", "", 10)
	if !ok || len(hits) != 1 || hits[0].Files != 2 || hits[0].Kind != "film" {
		t.Fatalf("recherche film : %+v", hits)
	}
	if hits, _ := root.searchCatalog("gilmore", "film", 10); len(hits) != 0 {
		t.Errorf("filtre film : %+v", hits)
	}
	ft, _ := root.catalogTitle("film", 1, -1)
	if ft == nil || len(ft.Files) != 2 || ft.Files[0].Bucket != "2160" || ft.Files[0].Probe.CodecVideo != "hevc" {
		t.Fatalf("film : %+v", ft)
	}
	st, _ := root.catalogTitle("série", 2, -1)
	if st == nil || st.Season != 1 || len(st.Seasons) != 2 || len(st.Files) != 2 || st.Files[0].Episode != 1 {
		t.Fatalf("série (saison par défaut, hors spéciaux) : %+v", st)
	}
	if sp, _ := root.catalogTitle("série", 2, 0); sp == nil || sp.Season != 0 || len(sp.Files) != 1 {
		t.Fatalf("saison 0 demandée : %+v", sp)
	}
	if none, ok := root.catalogTitle("film", 99, -1); !ok || none != nil {
		t.Error("titre inconnu")
	}
}
