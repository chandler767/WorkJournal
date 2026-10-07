package web

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"workjournal/internal/config"
	"workjournal/internal/diarize"
)

// settingsEnv wires a Cfg/SaveCfg pair that records saves like the real
// daemon (validate, then swap the live config).
type settingsEnv struct {
	*env
	cfg   config.Config
	saves []config.Config
}

func newSettingsEnv(t *testing.T) *settingsEnv {
	t.Helper()
	e := &settingsEnv{env: newEnv(t), cfg: config.Default()}
	e.srv.Cfg = func() config.Config { return e.cfg }
	e.srv.SaveCfg = func(c config.Config) error {
		if err := c.Validate(); err != nil {
			return err
		}
		e.saves = append(e.saves, c)
		e.cfg = c
		return nil
	}
	e.srv.Voices = &diarize.Voices{Dir: e.p.Voices(), Threshold: 0.75}
	return e
}

func (e *settingsEnv) form() url.Values {
	c := e.cfg
	return url.Values{
		"retention_days":    {"7"},
		"close_time":        {c.CloseTime},
		"rollup_weekday":    {c.RollupWeekday},
		"idle_minutes":      {"5"},
		"pause_auto_resume": {"60"},
		"excluded_apps":     {"1Password\nKeychain Access"},
		"excluded_titles":   {"Private"},
		"categories":        {strings.Join(c.Categories, "\n")},
		"rules":             {""},
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	e := newSettingsEnv(t)
	f := e.form()
	f.Set("retention_days", "14")
	f.Set("excluded_apps", "  Slack \n\n Mail\n")
	f.Set("rules", "Safari = Browsing/Research\nCode = Deep work")
	rec := e.post("/settings", f, nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings?saved=1" {
		t.Fatalf("got %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if len(e.saves) != 1 || e.saves[0].RetentionDays != 14 {
		t.Fatalf("saves = %+v", e.saves)
	}
	if got := strings.Join(e.saves[0].ExcludedApps, "|"); got != "Slack|Mail" {
		t.Errorf("excluded = %q", got)
	}
	if e.saves[0].Rules["Safari"] != "Browsing/Research" || e.saves[0].Rules["Code"] != "Deep work" {
		t.Errorf("rules = %v", e.saves[0].Rules)
	}
	body := e.get("/settings?saved=1").Body.String()
	mustContain(t, body, `value="14"`, "Saved", "restart", "Restart recorder")
}

func TestSettingsRejectsInvalid(t *testing.T) {
	e := newSettingsEnv(t)
	f := e.form()
	f.Set("retention_days", "0")
	f.Set("excluded_apps", "Mine")
	rec := e.post("/settings", f, nil)
	if rec.Code != 400 {
		t.Fatalf("code %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "retention_days", `value="0"`, "Mine")
	if len(e.saves) != 0 {
		t.Fatal("SaveCfg reached with invalid input")
	}
}

func TestSettingsRejectsBadNumberAndRules(t *testing.T) {
	e := newSettingsEnv(t)
	f := e.form()
	f.Set("retention_days", "abc")
	if c := e.post("/settings", f, nil).Code; c != 400 {
		t.Errorf("non-numeric = %d", c)
	}
	f = e.form()
	f.Set("rules", "Safari = Browsing/Research\nno equals here")
	rec := e.post("/settings", f, nil)
	if rec.Code != 400 {
		t.Fatalf("malformed rule = %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "line 2")
	f.Set("rules", "Safari = Nonexistent")
	rec = e.post("/settings", f, nil)
	if rec.Code != 400 {
		t.Fatalf("unknown category = %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Nonexistent")
	if len(e.saves) != 0 {
		t.Fatal("SaveCfg reached")
	}
}

func TestSettingsCrossOriginRefused(t *testing.T) {
	e := newSettingsEnv(t)
	rec := e.post("/settings", e.form(), map[string]string{"Origin": "http://evil.example"})
	if rec.Code != 403 || len(e.saves) != 0 {
		t.Fatalf("code %d saves %d", rec.Code, len(e.saves))
	}
}

func TestAcceptRuleSuggestion(t *testing.T) {
	e := newSettingsEnv(t)
	if err := e.st.SetKV("rule_suggestions", `[{"app":"Safari","category":"Deep work"},{"app":"Mail","category":"Admin"}]`); err != nil {
		t.Fatal(err)
	}
	body := e.get("/settings").Body.String()
	mustContain(t, body, "Safari", "Mail")
	rec := e.post("/settings/rule-suggestion/accept", url.Values{"app": {"Safari"}, "category": {"Deep work"}}, nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(e.saves) != 1 || e.saves[0].Rules["Safari"] != "Deep work" {
		t.Fatalf("saves = %+v", e.saves)
	}
	v, _, _ := e.st.GetKV("rule_suggestions")
	if strings.Contains(v, "Safari") || !strings.Contains(v, "Mail") {
		t.Fatalf("kv = %s", v)
	}
	// dismiss only touches KV
	rec = e.post("/settings/rule-suggestion/dismiss", url.Values{"app": {"Mail"}}, nil)
	if rec.Code != 303 || len(e.saves) != 1 {
		t.Fatalf("dismiss: %d saves %d", rec.Code, len(e.saves))
	}
	v, _, _ = e.st.GetKV("rule_suggestions")
	if strings.Contains(v, "Mail") {
		t.Fatalf("kv = %s", v)
	}
	if c := e.post("/settings/rule-suggestion/bogus", url.Values{"app": {"x"}}, nil).Code; c != 404 {
		t.Errorf("bogus action = %d", c)
	}
}

func TestDeleteVoice(t *testing.T) {
	e := newSettingsEnv(t)
	if err := e.srv.Voices.Enroll("Priya", []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	mustContain(t, e.get("/settings").Body.String(), "Priya")
	if c := e.post("/settings/voice/Nobody/delete", url.Values{}, nil).Code; c != 404 {
		t.Errorf("unknown voice = %d", c)
	}
	rec := e.post("/settings/voice/Priya/delete", url.Values{}, nil)
	if rec.Code != 303 {
		t.Fatalf("code %d", rec.Code)
	}
	if names, _ := e.srv.Voices.List(); len(names) != 0 {
		t.Fatalf("voices = %v", names)
	}
}

func TestVoicesHiddenWhenDiarizationOff(t *testing.T) {
	e := newSettingsEnv(t)
	if err := e.srv.Voices.Enroll("Priya", []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	e.cfg.Diarization = "off"
	body := e.get("/settings").Body.String()
	if strings.Contains(body, "Priya") || strings.Contains(body, "/voice/") {
		t.Error("voice list shown with diarization off")
	}
}

func TestRestartRecorder(t *testing.T) {
	e := newSettingsEnv(t)
	rec := e.post("/settings/restart-recorder", url.Values{}, nil)
	if rec.Code != 303 || len(e.ctl.sent) != 1 || e.ctl.sent[0] != "quit" {
		t.Fatalf("code %d sent %v", rec.Code, e.ctl.sent)
	}
	mustContain(t, e.get(rec.Header().Get("Location")).Body.String(), "Recorder restarting")
	e.ctl.sendErr = errors.New("dial recorder")
	rec = e.post("/settings/restart-recorder", url.Values{}, nil)
	mustContain(t, e.get(rec.Header().Get("Location")).Body.String(), "isn&#39;t running")
}

func TestSettingsNavLink(t *testing.T) {
	e := newSettingsEnv(t)
	mustContain(t, e.get("/search").Body.String(), `href="/settings"`)
}
