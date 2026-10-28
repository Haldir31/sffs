package main

import "testing"

func TestSubstituteRank(t *testing.T) {
	av1 := "Gladiator.2000.MULTi.VFF.1080p.DV.BluRay.HDlight.AC3.AV1-TiFou"
	hevc := "Gladiator 2000 MULTi VFi 1080p BluRay HDR10 E-AC3 x265-Winks"
	avc := "Gladiator.2000.Theatrical.Cut.MULTI.VFF.1080p.WEB.EAC3.5.1.H264-FW"
	if !(substituteRank("hevc", hevc) < substituteRank("hevc", avc) && substituteRank("hevc", avc) < substituteRank("hevc", av1)) {
		t.Error("original HEVC : HEVC, puis H.264, l'AV1 en dernier")
	}
	if substituteRank("av1", av1) != 0 {
		t.Error("original AV1 : l'AV1 en tête")
	}
	if codecOfName(av1) != "av1" || codecOfName(hevc) != "hevc" || codecOfName(avc) != "h264" || codecOfName("Film.1080p.BluRay") != "" {
		t.Error("codecOfName")
	}
}
