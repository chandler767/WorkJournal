package web

import (
	"errors"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAudioModePostSetsRecorderAndSaves(t *testing.T) {
	e := newSettingsEnv(t)
	e.ctl.audioMode = "on"
	rec := e.post("/audio-mode", url.Values{"mode": {"auto"}}, map[string]string{"Referer": "http://127.0.0.1:4747/day/" + seedDay})
	if rec.Code != 303 || rec.Header().Get("Location") != "/day/"+seedDay {
		t.Fatalf("code %d location %q body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if !slices.Equal(e.ctl.calls, []string{"audio_mode auto"}) {
		t.Errorf("recorder calls %v", e.ctl.calls)
	}
	if len(e.saves) != 1 || e.cfg.AudioMode != "auto" {
		t.Errorf("saves %d, audio_mode %q", len(e.saves), e.cfg.AudioMode)
	}
}

func TestAudioModePostRejectsBadInput(t *testing.T) {
	e := newSettingsEnv(t)
	for _, m := range []string{"", "loud", "AUTO"} {
		if rec := e.post("/audio-mode", url.Values{"mode": {m}}, nil); rec.Code != 400 {
			t.Errorf("mode %q: code %d", m, rec.Code)
		}
	}
	if rec := e.post("/audio-mode", url.Values{"mode": {"off"}}, map[string]string{"Origin": "http://evil.example"}); rec.Code != 403 {
		t.Errorf("cross-origin: code %d", rec.Code)
	}
	if len(e.ctl.calls) != 0 || len(e.saves) != 0 {
		t.Errorf("calls %v saves %d", e.ctl.calls, len(e.saves))
	}
}

func TestAudioModePostRecorderErrorNotSaved(t *testing.T) {
	e := newSettingsEnv(t)
	e.ctl.modeErr = errors.New("dial recorder: no socket")
	rec := e.post("/audio-mode", url.Values{"mode": {"off"}}, nil)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "no socket") {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if len(e.saves) != 0 {
		t.Errorf("saved %+v although the recorder refused", e.saves)
	}
}

func TestDayPageShowsAudioModeControl(t *testing.T) {
	cases := []struct {
		mode, reason, app string
		active            bool
		want              string
	}{
		{"on", "", "", true, "Audio: On"},
		{"off", "", "", false, "Audio: Off"},
		{"auto", "", "", false, "Audio: Auto · listening"},
		{"auto", "call", "", true, "Audio: Auto · recording (call)"},
		{"auto", "call", "Zoom", true, "Audio: Auto · recording (Zoom)"},
		{"auto", "in_person", "", true, "Audio: Auto · recording (in person)"},
	}
	for _, c := range cases {
		e := newEnv(t)
		e.ctl.audioMode, e.ctl.audioReason, e.ctl.audioActive, e.ctl.audioApp = c.mode, c.reason, c.active, c.app
		body := e.get("/day/" + seedDay).Body.String()
		mustContain(t, body, c.want, `action="/audio-mode"`,
			`name="mode" value="on"`, `name="mode" value="off"`, `name="mode" value="auto"`)
		// The current mode's button is marked pressed.
		if !strings.Contains(body, `value="`+c.mode+`" aria-pressed="true"`) {
			t.Errorf("%s: current mode not marked pressed", c.mode)
		}
	}
	// No 3-way control when the recorder is down.
	e := newEnv(t)
	e.ctl.statusErr = errors.New("down")
	if body := e.get("/day/" + seedDay).Body.String(); strings.Contains(body, `action="/audio-mode"`) {
		t.Error("audio control shown with the recorder down")
	}
}

func TestDayRevChangesWithAudioMode(t *testing.T) {
	e := newEnv(t)
	e.ctl.audioMode = "auto"
	day := "/api/rev/day/" + seedDay
	base := revOf(t, e, day)
	e.ctl.audioActive, e.ctl.audioReason = true, "call"
	active := revOf(t, e, day)
	if active == base {
		t.Error("rev unchanged when auto started recording")
	}
	e.ctl.audioApp = "Zoom"
	withApp := revOf(t, e, day)
	if withApp == active {
		t.Error("rev unchanged when the call app became known")
	}
	e.ctl.audioMode, e.ctl.audioActive, e.ctl.audioReason, e.ctl.audioApp = "off", false, "", ""
	if revOf(t, e, day) == withApp {
		t.Error("rev unchanged when the mode changed")
	}
}

func TestSettingsAudioMode(t *testing.T) {
	e := newSettingsEnv(t)
	body := e.get("/settings").Body.String()
	mustContain(t, body, `name="audio_mode"`, `<option value="on" selected>`, `<option value="auto">`)

	f := e.form()
	f.Set("audio_mode", "auto")
	if rec := e.post("/settings", f, nil); rec.Code != 303 {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if e.cfg.AudioMode != "auto" {
		t.Errorf("audio_mode %q", e.cfg.AudioMode)
	}
	// A changed mode applies to the running recorder too, or the next sync
	// would copy the recorder's old mode back into the config.
	if !slices.Equal(e.ctl.calls, []string{"audio_mode auto"}) {
		t.Errorf("recorder calls %v", e.ctl.calls)
	}
	// Unchanged mode: the recorder is not told again.
	if rec := e.post("/settings", f, nil); rec.Code != 303 || len(e.ctl.calls) != 1 {
		t.Errorf("code %d calls %v", rec.Code, e.ctl.calls)
	}
	// Absent from an older form: kept.
	f.Del("audio_mode")
	if rec := e.post("/settings", f, nil); rec.Code != 303 || e.cfg.AudioMode != "auto" {
		t.Errorf("absent field: code %d mode %q", rec.Code, e.cfg.AudioMode)
	}
	f.Set("audio_mode", "loud")
	if rec := e.post("/settings", f, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "audio_mode") {
		t.Errorf("bad mode: code %d", rec.Code)
	}
}

// The hour page lists collision-suffixed chunks (HHMMSS-N-<track>.m4a) too.
func TestHourListsSuffixedChunks(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.p.Audio(seedDay), "090507-1-mic.m4a"), "audio")
	rows := e.srv.audioFor(seedDay, "09")
	found := false
	for _, r := range rows {
		if r.Name == "090507-1-mic.m4a" && r.Track == "mic" && r.Start == 5*60+7 {
			found = true
		}
	}
	if !found {
		t.Fatalf("suffixed chunk not listed: %+v", rows)
	}
}
