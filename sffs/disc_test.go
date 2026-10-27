package main

import "testing"

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
