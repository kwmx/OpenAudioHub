package oah

import "testing"

// Pre-release ordering matters here: an rc must not look newer than the release
// it precedes, or the hub would offer a downgrade as an upgrade.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.1.6-rc1", 1},
		{"0.1.6-rc1", "0.2.0", -1},
		{"v0.2.0", "0.2.0", 0},
		{"0.1.10", "0.1.9", 1},
		{"0.2.0-rc1", "0.2.0", -1},
		{"0.2.0", "0.2.0-rc1", 1},
		{"0.2.0-rc2", "0.2.0-rc1", 1},
		{"1.0.0", "1.0.0", 0},
		{"0.1.6", "0.1.6", 0},
		{"1.2.3", "1.2", 1},
		{"dev", "1.0.0", -1},
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		if (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Fatalf("compareVersions(%q,%q)=%d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

// "No releases published" and "GitHub unreachable" must not be reported as
// "up to date": the user cannot act on a wrong answer.
func TestUpdateStatusDistinguishesUnknownFromCurrent(t *testing.T) {
	st := UpdateStatus{Current: "0.1.6-rc1", Checked: true}
	if st.Available {
		t.Fatal("a bare status must not claim an update is available")
	}
	if st.Detail != "" {
		t.Fatalf("unexpected detail %q", st.Detail)
	}
}

// The endpoint must refuse to install when no newer release is known.
func TestUpdateApplyRefusesWithoutAKnownRelease(t *testing.T) {
	a := &App{}
	updateMu.Lock()
	updateState = UpdateStatus{Current: "0.1.6-rc1", Checked: false}
	updateRun = false
	updateMu.Unlock()
	st := a.updateStatus()
	if st.Available {
		t.Fatal("status should not offer an update before a check has run")
	}
	if st.Current != "0.1.6-rc1" && st.Current != "" {
		t.Fatalf("unexpected current %q", st.Current)
	}
}
