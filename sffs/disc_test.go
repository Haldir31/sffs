package main

import (
	"fmt"
	"testing"
)

func TestUnplayable(t *testing.T) {
	for raw, want := range map[string]bool{
		"1917.2019.MULTI.VFF.2160p.UHD.BluRay.ISO.DV.HDR10PLUS.TrueHD.7.1.HEVC-Kaoru111":   true,
		"Wander.2020.COMPLETE.BLURAY-UNTOUCHED":                                            true,
		"The.Falcon.and.the.Winter.Soldier.S01D01.COMPLETE.UHD.BLURAY-REFRACTiON":          true,
		"Portrait.De.Femme.1996.MULTI.VFF.1080p.Bluray.BDMV.DTS.HD.MA.5.1.AVC-FgCw":        true,
		"Anaconda.1997.MULTi.VF2.2160p.Full.BluRay.DV.HDR10.TrueHD.Atmos.7.1.HEVC-SURCODE": true,
		"A.Normal.Family.2023.VOSTFR.1080p.FRA.Full.Bluray.AVC.DTS-HD.MA.5.1-GWXHBL":       true,
		"Spivet 3D 2013 Multi-True-French 1080p Full Bluray AVC DTS":                       true,
		"Dune.2021.MULTi.VFF.2160p.BluRay.FULL.UHD.BluRay-NoTag":                           true,
		"Fullmetal.Jacket.1987.MULTi.1080p.BluRay.x264":                                    false,
		"Alien.Predator.2018.FRENCH.1080p.BluRay.Remux.AVC.DTS.HDMA.5.1-Shamir":            false,
		"Isolation.2005.FRENCH.1080p.BluRay.x264":                                          false,
		"Prisoners.2013.MULTI.2160p.UHD.BluRay.x265-ISOTOPE":                               false,
		"12.Strong.2018.FRENCH.720p.BluRay.x264-VENUE":                                     false,
	} {
		if got := unplayable(raw, ""); got != want {
			t.Errorf("%s : %v, attendu %v", raw, got, want)
		}
	}
}

func TestCachedOnly(t *testing.T) {
	vs := []*version{
		{id: "a", bucket: "1080", available: true, releaseKey: "a"},
		{id: "b", bucket: "1080", available: false, releaseKey: "b"},
		{id: "c", bucket: "2160", available: false, releaseKey: "c"},
	}
	cfg := config{langs: []string{"fr"}, cachedOnly: true}
	got := selectVersions(vs, cfg, 0, 5)
	if len(got) != 1 || got[0].id != "a" {
		t.Fatalf("en cache seulement : %d versions, attendu la seule version en cache", len(got))
	}
	cfg.cachedOnly = false
	if got := selectVersions(vs, cfg, 0, 5); len(got) != 3 {
		t.Fatalf("mode normal : %d versions, attendu 3", len(got))
	}
}

func TestLightAndRemux(t *testing.T) {
	light := map[string]bool{
		"1917.2019.MULTI.VFF.1080p.BluRay.HDLight.AC3.5.1.x265-XSHD":         true,
		"1917.2019.MULTI.VFF.2160p.4KLight.HDR10Plus.EAC3.5.1.x265-Neostark": true,
		"Film.2020.MULTI.1080p.mHD.x264.AC3":                                 true,
		"Romeo.&.Juliet.1968.MULTi.VFF.1080p.BluRay.Light.AC3.x264-ZT":       true,
		"La.Traversee.2022.VFF.1080p.LIGHT.BLURAY.x264.DDP-SbR":              true,
		"A.Small.Light.S01E07.MULTi.1080p.WEB.H264-FW":                       false,
		"Charmed.2018.S03E15.FRENCH.LD.AMZN.WEB-DL.x264-FRATERNiTY":          false,
		"Lightyear.2022.MULTi.2160p.UHD.BluRay.Remux.HDR.HEVC":               false,
	}
	for raw, want := range light {
		if got := isLight(raw, ""); got != want {
			t.Errorf("isLight(%s) = %v, attendu %v", raw, got, want)
		}
	}
	remux := "Alien.Predator.2018.FRENCH.1080p.BluRay.Remux.AVC.DTS.HDMA.5.1-Shamir"
	hdl := "1917.2019.MULTI.VFF.1080p.BluRay.HDLight.AC3.5.1.x265-XSHD"
	plain := "12.Strong.2018.FRENCH.720p.BluRay.x264-VENUE"
	def := config{remux: true}
	if excludedByChoice(def, remux, "") || !excludedByChoice(def, hdl, "") || excludedByChoice(def, plain, "") {
		t.Error("réglages par défaut : REMUX gardé, encodage léger écarté, autres gardés")
	}
	if !excludedByChoice(config{remux: false, light: true}, remux, "") || excludedByChoice(config{remux: false, light: true}, hdl, "") {
		t.Error("SFFS_REMUX=0 SFFS_LIGHT=1 : REMUX écarté, encodage léger gardé")
	}
}

func TestNoQuota(t *testing.T) {
	var vs []*version
	for i := 0; i < 8; i++ {
		vs = append(vs, &version{id: fmt.Sprint("fr", i), bucket: "1080", releaseKey: fmt.Sprint("r", i), langScore: 0})
	}
	vs = append(vs,
		&version{id: "dup", bucket: "1080", releaseKey: "r0", langScore: 0}, // même release
		&version{id: "en", bucket: "1080", releaseKey: "en", langScore: 1},  // autre langue
		&version{id: "old", bucket: "1080", releaseKey: "old", year: 1990},  // mauvaise année
	)
	cfg := config{langs: []string{"fr"}}
	if got := selectVersions(vs, cfg, 2020, 5); len(got) != 5 {
		t.Fatalf("quota 5 : %d versions", len(got))
	}
	got := selectVersions(vs, cfg, 2020, 0)
	if len(got) != 8 {
		t.Fatalf("sans quota : %d versions, attendu les 8 releases en français", len(got))
	}
	for _, v := range got {
		if v.id == "dup" || v.id == "en" || v.id == "old" {
			t.Errorf("sans quota : %s ne devait pas être gardée", v.id)
		}
	}
}
