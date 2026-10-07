package main

import (
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"workjournal/internal/config"
)

// agentLabel names the per-user LaunchAgent that runs `journal serve` at login.
const agentLabel = "local.workjournal.serve"

// agentSpec is what the LaunchAgent runs and where it logs.
type agentSpec struct {
	Journal string // absolute path to bin/journal
	Config  string // absolute config path
	Path    string // PATH for serve (whisper-cli, claude, tessl live outside launchd's default PATH)
	Log     string // stdout+stderr of serve
}

type autostartDeps struct {
	AgentsDir string // ~/Library/LaunchAgents
	UID       int
	Launchctl func(args ...string) ([]byte, error)
	Spec      agentSpec
}

func defaultAutostartDeps(cfgPath string, cfg config.Config) (autostartDeps, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return autostartDeps{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return autostartDeps{}, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return autostartDeps{}, err
	}
	absCfg, err := filepath.Abs(cfgPath)
	if err != nil {
		return autostartDeps{}, err
	}
	return autostartDeps{
		AgentsDir: filepath.Join(home, "Library", "LaunchAgents"),
		UID:       os.Getuid(),
		Launchctl: func(args ...string) ([]byte, error) {
			return exec.Command("/bin/launchctl", args...).CombinedOutput()
		},
		Spec: agentSpec{
			Journal: exe,
			Config:  absCfg,
			Path:    agentPath(os.Getenv("PATH"), home),
			Log:     filepath.Join(config.Paths{Root: cfg.DataDir}.Logs(), "serve.log"),
		},
	}, nil
}

// runAutostart handles `journal autostart on|off|status`. "on" also starts serve
// now; "off" also stops it now.
func runAutostart(action string, d autostartDeps, out io.Writer) error {
	plist := filepath.Join(d.AgentsDir, agentLabel+".plist")
	domain := fmt.Sprintf("gui/%d", d.UID)
	service := domain + "/" + agentLabel
	switch action {
	case "on":
		if err := os.MkdirAll(d.AgentsDir, 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(d.Spec.Log), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(plist, []byte(launchAgentPlist(d.Spec)), 0o644); err != nil {
			return err
		}
		_, _ = d.Launchctl("bootout", service) // reload if already loaded; "not loaded" is fine
		if b, err := d.Launchctl("bootstrap", domain, plist); err != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(b)))
		}
		fmt.Fprintf(out, "autostart on: `journal serve` starts at login and is running now (log: %s)\n", d.Spec.Log)
		return nil
	case "off":
		_, _ = d.Launchctl("bootout", service)
		if err := os.Remove(plist); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		fmt.Fprintln(out, "autostart off: serve stopped and will not start at login")
		return nil
	case "status":
		if _, err := os.Stat(plist); errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(out, "autostart: not installed")
			return nil
		}
		state := "not running"
		if b, err := d.Launchctl("print", service); err == nil && strings.Contains(string(b), "state = running") {
			state = "running"
		}
		fmt.Fprintf(out, "autostart: installed (%s), %s\n", plist, state)
		return nil
	}
	return fmt.Errorf("unknown action %q (want on, off or status)", action)
}

func launchAgentPlist(s agentSpec) string {
	e := html.EscapeString
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + agentLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + e(s.Journal) + `</string>
		<string>serve</string>
		<string>--config</string>
		<string>` + e(s.Config) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>` + e(s.Path) + `</string>
	</dict>
	<key>StandardOutPath</key>
	<string>` + e(s.Log) + `</string>
	<key>StandardErrorPath</key>
	<string>` + e(s.Log) + `</string>
</dict>
</plist>
`
}

// agentPath keeps the caller's PATH and appends the usual tool locations
// (Homebrew for whisper-cli, ~/.local/bin for claude), since launchd agents
// don't load shell profiles and the terminal running `autostart on` may not have.
func agentPath(current, home string) string {
	var dirs []string
	seen := map[string]bool{}
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	for _, d := range strings.Split(current, ":") {
		add(d)
	}
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local", "bin"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		add(d)
	}
	return strings.Join(dirs, ":")
}
