package oah

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UpdateStatus is the running version compared against the newest published
// GitHub release. Nothing here mutates the appliance.
type UpdateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Checked   bool   `json:"checked"`
	Notes     string `json:"notes,omitempty"`
	Published string `json:"published,omitempty"`
	URL       string `json:"url,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Running   bool   `json:"running"`
}

// updateUnit is the transient systemd unit that performs the install. It must not
// be a child of the daemon, which the installer restarts.
const updateUnit = "openaudiohub-update"

func updateRepo() string {
	if v := strings.TrimSpace(getenv("OPENAUDIOHUB_REPO")); v != "" {
		return v
	}
	return "kwmx/OpenAudioHub"
}

// compareVersions orders two release strings. A leading "v" is ignored and any
// pre-release suffix ranks below the same number without one, so 0.2.0-rc1 is
// older than 0.2.0 but newer than 0.1.9.
func compareVersions(a, b string) int {
	split := func(v string) ([]int, string) {
		v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
		pre := ""
		if i := strings.IndexAny(v, "-+"); i >= 0 {
			pre = v[i+1:]
			v = v[:i]
		}
		parts := strings.Split(v, ".")
		nums := make([]int, 0, len(parts))
		for _, p := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				n = 0
			}
			nums = append(nums, n)
		}
		return nums, pre
	}
	an, ap := split(a)
	bn, bp := split(b)
	for i := 0; i < len(an) || i < len(bn); i++ {
		var x, y int
		if i < len(an) {
			x = an[i]
		}
		if i < len(bn) {
			y = bn[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1 // release outranks its pre-release
	case bp == "":
		return -1
	case ap < bp:
		return -1
	default:
		return 1
	}
}

var (
	updateMu     sync.Mutex
	updateState  UpdateStatus
	updateRanAt  time.Time
	updateRun    bool
	updateNotice string
)

func getenv(k string) string { return os.Getenv(k) }

// checkLatestRelease queries GitHub once. Errors are reported rather than
// swallowed: "could not check" and "up to date" are different answers and the
// user needs to be able to tell them apart.
func (a *App) checkLatestRelease() UpdateStatus {
	st := UpdateStatus{Current: a.version, Checked: true}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+updateRepo()+"/releases/latest", nil)
	if err != nil {
		st.Detail = "Could not build the update request."
		return st
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	cl := &http.Client{Timeout: 12 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		st.Detail = "Could not reach GitHub to check for releases."
		return st
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// No releases published yet is a normal state, not a failure.
		st.Detail = "No releases have been published yet."
		return st
	}
	if resp.StatusCode != http.StatusOK {
		st.Detail = fmt.Sprintf("GitHub returned %d when checking for releases.", resp.StatusCode)
		return st
	}
	var rel struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		HTMLURL     string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		st.Detail = "GitHub sent a release this build could not read."
		return st
	}
	st.Latest = rel.TagName
	st.Published = rel.PublishedAt
	st.URL = rel.HTMLURL
	st.Notes = strings.TrimSpace(rel.Body)
	if len(st.Notes) > 800 {
		st.Notes = st.Notes[:800] + "…"
	}
	if a.version == "dev" || a.version == "" {
		st.Detail = "This build reports no version, so it cannot be compared with a release."
		return st
	}
	st.Available = compareVersions(st.Latest, a.version) > 0
	if !st.Available {
		st.Detail = "This hub is running the newest published release."
	}
	return st
}

func (a *App) updateStatus() UpdateStatus {
	updateMu.Lock()
	defer updateMu.Unlock()
	st := updateState
	st.Running = updateRun
	if st.Current == "" {
		st.Current = a.version
	}
	return st
}

// handleUpdateCheck performs the GitHub lookup on demand rather than on every
// state build: it is a network call, and the dashboard must stay cheap.
func (a *App) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	st := a.checkLatestRelease()
	updateMu.Lock()
	updateState = st
	updateRanAt = time.Now()
	updateMu.Unlock()
	writeJSON(w, 200, st)
}

// handleUpdateApply runs the installed update script. It is deliberately a script
// rather than in-process logic so the same path can be run by hand over SSH, and
// so the daemon never rewrites its own binary while executing.
func (a *App) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	updateMu.Lock()
	if updateRun {
		updateMu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "An update is already running."})
		return
	}
	st := updateState
	if !st.Checked || !st.Available {
		updateMu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "Check for updates first; nothing newer is known."})
		return
	}
	updateRun = true
	updateMu.Unlock()

	// The installer stops this service. Running update.sh as a child of this unit
	// put it in the same cgroup, so systemd killed the updater the moment the
	// installer stopped the daemon: the service went down and never came back. A
	// transient unit is independent of this one, so it survives the restart it
	// performs. Progress lands in the journal under OPENAUDIOHUB_UPDATE_UNIT.
	a.logf("update: starting %s (from %s) as unit %s", st.Latest, a.version, updateUnit)
	if _, err := a.run.Run(30*time.Second, "systemd-run",
		"--unit="+updateUnit,
		"--description=OpenAudioHub release update",
		"--collect",
		"/usr/local/lib/openaudiohub/update.sh", "install", st.Latest); err != nil {
		updateMu.Lock()
		updateRun = false
		updateNotice = "The update could not be started. Inspect Diagnostics."
		updateMu.Unlock()
		a.logf("update failed to start: %v", err)
		a.signalRefresh()
		return
	}
	updateMu.Lock()
	updateNotice = "Installing " + st.Latest + ". The hub restarts itself; this page will reconnect."
	updateMu.Unlock()
	a.signalRefresh()

	writeJSON(w, 202, map[string]any{"ok": true, "installing": st.Latest})
}
