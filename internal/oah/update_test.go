package oah

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
	rec := httptest.NewRecorder()
	a.handleUpdateApply(rec, httptest.NewRequest("POST", "/api/system/update", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	updateMu.Lock()
	running := updateRun
	updateMu.Unlock()
	if running {
		t.Fatal("a refused update must not be marked as running")
	}
}

func TestUpdateProgressLines(t *testing.T) {
	out := "Installing OpenAudioHub v1.0.7 (currently 1.0.6)\nDownloading https://github.com/x/archive/refs/tags/v1.0.7.tar.gz\n" +
		"Running the installer from the release archive\n[1/9] Installing runtime packages...\nGet:1 http://deb.debian.org trixie InRelease\n" +
		"[2/9] Installing files...\n\n"
	step, last := updateProgressLines(out)
	if step != "[2/9] Installing files..." || last != "[2/9] Installing files..." {
		t.Fatalf("step %q last %q", step, last)
	}
	step, last = updateProgressLines("Downloading https://example\nDownload failed. Check the network and the tag name.\n")
	if step != "Downloading https://example" || last != "Download failed. Check the network and the tag name." {
		t.Fatalf("failure: step %q last %q", step, last)
	}
	if step, last = updateProgressLines("-- No entries --\n"); step != "" || last != "" {
		t.Fatalf("journalctl notes are not progress: %q %q", step, last)
	}
}

func TestUpdateProgressEndpoint(t *testing.T) {
	rec := &recordedRunner{reply: func(call string) (string, error) {
		switch {
		case call == "systemctl is-active openaudiohub-update.service":
			return "active\n", nil
		case strings.HasPrefix(call, "journalctl -u openaudiohub-update.service"):
			return "[7/9] Applying audio configuration...\n", nil
		}
		return "", nil
	}}
	a := &App{run: runner{fake: rec.run}, version: "1.0.6"}
	updateMu.Lock()
	updateState = UpdateStatus{Latest: "v1.0.7"}
	updateRun = false
	updateMu.Unlock()
	w := httptest.NewRecorder()
	a.handleUpdateProgress(w, httptest.NewRequest("GET", "/api/system/update/status?since=1760000000", nil))
	var p UpdateProgress
	if err := json.NewDecoder(w.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if !p.Running || p.Current != "1.0.6" || p.Target != "v1.0.7" || p.Step != "[7/9] Applying audio configuration..." {
		t.Fatalf("progress %+v", p)
	}
	if !rec.has("journalctl -u openaudiohub-update.service -n 40 -o cat --no-pager --since @1760000000") {
		t.Fatalf("journal must be limited to this attempt, calls: %v", rec.calls)
	}
}
