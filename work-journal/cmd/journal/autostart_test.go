package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// fakeLaunchctl models launchd: bootout starts unloading the service, which
// stays visible to `print` for unloadPrints more calls; bootstrap loads it
// unless bootstrapFails says to fail with launchd's "5: Input/output error".
type fakeLaunchctl struct {
	calls          []string
	failBootout    bool
	loaded         bool
	unloadPrints   int
	pendingUnload  int
	bootstrapFails int
	sleeps         []time.Duration
}

func (f *fakeLaunchctl) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "bootout":
		if f.loaded {
			f.pendingUnload = f.unloadPrints
		}
		f.loaded = false
		if f.failBootout {
			return nil, errors.New("not loaded")
		}
	case "print":
		if f.pendingUnload > 0 {
			f.pendingUnload--
			return []byte("state = not running\n"), nil
		}
		if !f.loaded {
			return []byte("Could not find service"), errors.New("exit status 113")
		}
		return []byte("state = running\npid = 42\n"), nil
	case "bootstrap":
		if f.bootstrapFails > 0 {
			f.bootstrapFails--
			return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
		}
		f.loaded = true
	}
	return nil, nil
}

func (f *fakeLaunchctl) sleep(d time.Duration) { f.sleeps = append(f.sleeps, d) }

func (f *fakeLaunchctl) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func newAutostartEnv(t *testing.T) (autostartDeps, *fakeLaunchctl, string) {
	dir := t.TempDir()
	fl := &fakeLaunchctl{failBootout: true}
	return autostartDeps{
		AgentsDir: filepath.Join(dir, "LaunchAgents"),
		UID:       501,
		Launchctl: fl.run,
		Sleep:     fl.sleep,
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
	want := []string{"bootout gui/501/" + agentLabel, "print gui/501/" + agentLabel, "bootstrap gui/501 " + plist}
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

// Reloading a running agent: launchd is still unloading the old instance
// right after bootout, and bootstrapping then fails with "5: Input/output
// error", so `on` waits until print stops finding the service.
func TestAutostartOnWaitsForUnload(t *testing.T) {
	d, fl, _ := newAutostartEnv(t)
	fl.failBootout, fl.loaded, fl.unloadPrints = false, true, 3
	if err := runAutostart("on", d, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if n := fl.count("print "); n != 4 {
		t.Errorf("print calls = %d, want 4 (3 still loaded, then gone): %v", n, fl.calls)
	}
	if last := fl.calls[len(fl.calls)-1]; !strings.HasPrefix(last, "bootstrap ") {
		t.Errorf("last call = %q, want bootstrap after the unload", last)
	}
	if len(fl.sleeps) != 3 {
		t.Errorf("sleeps = %v, want one per still-loaded poll", fl.sleeps)
	}
}

func TestAutostartOnStopsWaitingAfterTenSeconds(t *testing.T) {
	d, fl, _ := newAutostartEnv(t)
	fl.failBootout, fl.loaded, fl.unloadPrints = false, true, 1_000_000
	if err := runAutostart("on", d, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var waited time.Duration
	for _, s := range fl.sleeps {
		waited += s
	}
	if waited < 9*time.Second || waited > 10*time.Second {
		t.Errorf("waited %s for unload, want about 10s", waited)
	}
	if fl.count("bootstrap ") != 1 {
		t.Errorf("calls = %v, want bootstrap after giving up", fl.calls)
	}
}

func TestAutostartOnRetriesBootstrapOnce(t *testing.T) {
	d, fl, _ := newAutostartEnv(t)
	fl.bootstrapFails = 1
	if err := runAutostart("on", d, &bytes.Buffer{}); err != nil {
		t.Fatalf("one I/O error should be retried: %v", err)
	}
	if fl.count("bootstrap ") != 2 {
		t.Errorf("calls = %v, want 2 bootstraps", fl.calls)
	}
	if len(fl.sleeps) == 0 || fl.sleeps[len(fl.sleeps)-1] != 2*time.Second {
		t.Errorf("sleeps = %v, want 2s before the retry", fl.sleeps)
	}

	d, fl, _ = newAutostartEnv(t)
	fl.bootstrapFails = 2
	err := runAutostart("on", d, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "5: Input/output error") {
		t.Errorf("err = %v, want the bootstrap error after one retry", err)
	}
	if fl.count("bootstrap ") != 2 {
		t.Errorf("calls = %v, want exactly 2 bootstraps", fl.calls)
	}
}
