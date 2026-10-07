package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgentPlist(t *testing.T) {
	got := launchAgentPlist(agentSpec{
		Journal: "/repo/work-journal/bin/journal",
		Config:  "/Users/me/WorkJournal/config.toml",
		Path:    "/opt/homebrew/bin:/usr/bin",
		Log:     "/Users/me/WorkJournal/logs/serve.log",
	})
	for _, want := range []string{
		"<string>" + agentLabel + "</string>",
		"<string>/repo/work-journal/bin/journal</string>\n\t\t<string>serve</string>\n\t\t<string>--config</string>\n\t\t<string>/Users/me/WorkJournal/config.toml</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>SuccessfulExit</key>\n\t\t<false/>",
		"<key>PATH</key>\n\t\t<string>/opt/homebrew/bin:/usr/bin</string>",
		"<key>StandardErrorPath</key>\n\t<string>/Users/me/WorkJournal/logs/serve.log</string>",
		"<key>ProcessType</key>\n\t<string>Interactive</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q\n%s", want, got)
		}
	}
}

func TestLaunchAgentPlistEscapesXML(t *testing.T) {
	got := launchAgentPlist(agentSpec{Journal: "/a&b/<j>", Config: "c", Path: "p", Log: "l"})
	if !strings.Contains(got, "<string>/a&amp;b/&lt;j&gt;</string>") {
		t.Errorf("path not escaped:\n%s", got)
	}
}

type fakeLaunchctl struct {
	calls       []string
	failBootout bool
}

func (f *fakeLaunchctl) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == "bootout" && f.failBootout {
		return nil, errors.New("not loaded")
	}
	if args[0] == "print" {
		return []byte("state = running\npid = 42\n"), nil
	}
	return nil, nil
}

func newAutostartEnv(t *testing.T) (autostartDeps, *fakeLaunchctl, string) {
	dir := t.TempDir()
	fl := &fakeLaunchctl{failBootout: true}
	return autostartDeps{
		AgentsDir: filepath.Join(dir, "LaunchAgents"),
		UID:       501,
		Launchctl: fl.run,
		Spec:      agentSpec{Journal: "/j", Config: "/c", Path: "/p", Log: filepath.Join(dir, "logs", "serve.log")},
	}, fl, dir
}

func TestAutostartOnWritesPlistAndBootstraps(t *testing.T) {
	d, fl, _ := newAutostartEnv(t)
	var out bytes.Buffer
	if err := runAutostart("on", d, &out); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(d.AgentsDir, agentLabel+".plist")
	b, err := os.ReadFile(plist)
	if err != nil || !strings.Contains(string(b), "<string>/j</string>") {
		t.Fatalf("plist not written: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Dir(d.Spec.Log)); err != nil {
		t.Errorf("log dir not created: %v", err)
	}
	want := []string{"bootout gui/501/" + agentLabel, "bootstrap gui/501 " + plist}
	if strings.Join(fl.calls, "|") != strings.Join(want, "|") {
		t.Errorf("launchctl calls = %v, want %v", fl.calls, want)
	}
}

func TestAutostartOffBootsOutAndRemoves(t *testing.T) {
	d, fl, _ := newAutostartEnv(t)
	if err := runAutostart("on", d, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	fl.calls, fl.failBootout = nil, false
	if err := runAutostart("off", d, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.AgentsDir, agentLabel+".plist")); !os.IsNotExist(err) {
		t.Errorf("plist still present: %v", err)
	}
	if len(fl.calls) != 1 || fl.calls[0] != "bootout gui/501/"+agentLabel {
		t.Errorf("calls = %v", fl.calls)
	}
}

func TestAutostartOffWhenNotInstalledIsOK(t *testing.T) {
	d, _, _ := newAutostartEnv(t)
	if err := runAutostart("off", d, &bytes.Buffer{}); err != nil {
		t.Fatalf("off when not installed: %v", err)
	}
}

func TestAutostartStatus(t *testing.T) {
	d, _, _ := newAutostartEnv(t)
	var out bytes.Buffer
	if err := runAutostart("status", d, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not installed") {
		t.Errorf("status before on = %q", out.String())
	}
	_ = runAutostart("on", d, &bytes.Buffer{})
	out.Reset()
	if err := runAutostart("status", d, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "installed") || !strings.Contains(out.String(), "running") {
		t.Errorf("status after on = %q", out.String())
	}
}

func TestAutostartRejectsUnknownAction(t *testing.T) {
	d, _, _ := newAutostartEnv(t)
	if err := runAutostart("maybe", d, &bytes.Buffer{}); err == nil {
		t.Fatal("want error for unknown action")
	}
}

func TestAgentPathAddsToolDirs(t *testing.T) {
	got := agentPath("/usr/bin:/bin", "/Users/me")
	for _, want := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/Users/me/.local/bin", "/usr/bin", "/bin"} {
		if !strings.Contains(":"+got+":", ":"+want+":") {
			t.Errorf("agentPath = %q, missing %s", got, want)
		}
	}
	if strings.Count(got, "/usr/bin:") > 1 {
		t.Errorf("duplicate entries: %q", got)
	}
	if !strings.HasPrefix(got, "/usr/bin:/bin") {
		t.Errorf("existing order not kept first: %q", got)
	}
}
