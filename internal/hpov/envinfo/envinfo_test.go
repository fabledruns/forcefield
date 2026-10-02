package envinfo

import "testing"

func TestCalibrateClock(t *testing.T) {
	src, res := CalibrateClock()
	if src == "" || res <= 0 {
		t.Fatalf("clock calibration = %q, %d", src, res)
	}
	t.Logf("clock %s resolution %dns", src, res)
}

func TestCollectSmoke(t *testing.T) {
	info, err := Collect("")
	if err != nil {
		t.Fatal(err)
	}
	if info.Host.OS == "" || info.Host.Arch == "" || info.Host.HostID == "" {
		t.Fatalf("host incomplete: %+v", info.Host)
	}
	if info.Host.Clock.ResolutionNs <= 0 {
		t.Fatal("clock resolution missing")
	}
}
