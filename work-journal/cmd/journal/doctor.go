package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"workjournal/internal/config"
)

// Check is one doctor result. Fix says how to resolve a failed check.
type Check struct {
	Name string
	OK   bool
	Fix  string
}

// DoctorDeps holds everything the checks look at, so tests can fake them.
type DoctorDeps struct {
	Cfg         config.Config
	RecorderApp string // path to WorkJournalRecorder.app
	SkillsDir   string // directory whose .claude/skills holds the installed skills; "" if not found
	LookPath    func(name string) (string, error)
	Stat        func(path string) (fs.FileInfo, error)
	Glob        func(pattern string) ([]string, error)
	FreeGB      func(path string) (float64, error)
	SelfTest    func(app string) error // runs `recorder selftest`; nil error means "selftest ok"
}

const permissionsFix = "grant WorkJournalRecorder access in System Settings → Privacy & Security → " +
	"Screen & System Audio Recording, Microphone and Accessibility, then run `journal doctor` again. " +
	"Every rebuild of the recorder resets these grants (remove the stale entry and add the app again)"

// RunChecks runs every doctor check in order.
func RunChecks(d DoctorDeps) []Check {
	var cs []Check
	add := func(name string, ok bool, fix string) {
		if ok {
			fix = ""
		}
		cs = append(cs, Check{Name: name, OK: ok, Fix: fix})
	}

	_, err := d.LookPath("whisper-cli")
	add("whisper-cli", err == nil, "brew install whisper-cpp")

	model := modelPath(config.Paths{Root: d.Cfg.DataDir}, d.Cfg.WhisperModel)
	add("whisper model", nonEmpty(d.Stat, model),
		fmt.Sprintf("run `journal setup` to download %s into %s", filepath.Base(model), filepath.Dir(model)))

	if d.Cfg.Diarization != "off" {
		add("diarization", false, `speaker diarization is not available in this build; set diarization = "off" in config.toml`)
	}

	_, err = d.LookPath("claude")
	add("claude", err == nil, "install Claude Code (https://claude.com/claude-code) so `claude` is on PATH")

	add("skills", d.SkillsDir != "" && skillsInstalled(d),
		skillsFix(d.SkillsDir))

	bin := filepath.Join(d.RecorderApp, "Contents", "MacOS", "recorder")
	built := nonEmpty(d.Stat, bin)
	add("recorder app", built, "run `journal setup` (or ./recorder/build.sh from the module root) to build "+d.RecorderApp)

	if !built {
		add("recorder selftest", false, "build the recorder first")
	} else if err := d.SelfTest(d.RecorderApp); err != nil {
		add("recorder selftest", false, fmt.Sprintf("%v: %s", err, permissionsFix))
	} else {
		add("recorder selftest", true, "")
	}

	free, err := d.FreeGB(d.Cfg.DataDir)
	switch {
	case err != nil:
		add("free disk", false, fmt.Sprintf("cannot measure free space at %s: %v", d.Cfg.DataDir, err))
	default:
		add("free disk", free >= float64(d.Cfg.MinFreeGB),
			fmt.Sprintf("only %.1f GB free, min_free_gb is %d: free up space or lower min_free_gb", free, d.Cfg.MinFreeGB))
	}
	return cs
}

func nonEmpty(stat func(string) (fs.FileInfo, error), path string) bool {
	fi, err := stat(path)
	return err == nil && fi.Size() > 0
}

func skillsInstalled(d DoctorDeps) bool {
	m, err := d.Glob(filepath.Join(d.SkillsDir, ".claude", "skills", "*summarize-hour*"))
	return err == nil && len(m) > 0
}

func skillsFix(dir string) string {
	install := "run `journal setup`, or `tessl install file:./work-journal/skills/work-journal --yes` from the repo root"
	if dir == "" {
		return "no directory above the module has .claude/skills/tessl__summarize-hour: " + install + ", or pass --repo <repo root>"
	}
	return "skills missing under " + filepath.Join(dir, ".claude", "skills") + ": " + install
}

// printChecks writes ✓/✗ lines and reports whether every check passed.
func printChecks(w io.Writer, cs []Check) bool {
	ok := true
	for _, c := range cs {
		if c.OK {
			fmt.Fprintf(w, "✓ %s\n", c.Name)
			continue
		}
		ok = false
		fmt.Fprintf(w, "✗ %s\n    fix: %s\n", c.Name, c.Fix)
	}
	return ok
}

// selftestTimeout bounds one selftest run (it captures for about 5 s, plus
// any permission prompts the user answers).
const selftestTimeout = 2 * time.Minute

// recorderSelfTest launches `recorder selftest` through LaunchServices so the
// app's own permissions apply, and reads the result file it writes (stdout is
// not visible through `open`). run executes `/usr/bin/open` with args.
func recorderSelfTest(app string, run func(bin string, args []string) error) error {
	dir, err := os.MkdirTemp("", "wj-doctor-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	runErr := run("/usr/bin/open", []string{"-W", "-n", app, "--args", "selftest", "--data-dir", dir})
	b, err := os.ReadFile(filepath.Join(dir, "selftest.result"))
	if err != nil {
		if runErr != nil {
			return fmt.Errorf("selftest did not run: %v", runErr)
		}
		return fmt.Errorf("selftest wrote no result: %v", err)
	}
	if line := strings.TrimSpace(string(b)); line != "selftest ok" {
		return fmt.Errorf("%s", line)
	}
	return nil
}

func execOpen(bin string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), selftestTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// nearestExisting returns path or its closest existing ancestor, so free
// space can be measured before the data directory exists.
func nearestExisting(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}
