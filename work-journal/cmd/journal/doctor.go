package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
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
	// ClaudeResponds runs a trivial headless prompt; nil means claude answered.
	ClaudeResponds func() error
	// SpeakerSeparation diarizes a short synthesized two-voice recording with
	// the given models; nil means it found speaker turns.
	SpeakerSeparation func(segModel, embModel string) error
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

	if d.Cfg.Diarization == "sherpa" {
		models := config.Paths{Root: d.Cfg.DataDir}.Models()
		seg, emb := diarize.ModelPaths(models)
		haveModels := nonEmpty(d.Stat, seg) && nonEmpty(d.Stat, emb)
		add("speaker models", haveModels,
			"run `journal setup` to download the speaker diarization models into "+models)
		if haveModels {
			if err := d.SpeakerSeparation(seg, emb); err != nil {
				logPath := filepath.Join(config.Paths{Root: d.Cfg.DataDir}.Logs(), "serve.log")
				add("speaker separation works", false, fmt.Sprintf(
					"%v: speakers will be unknown; look in %s for \"WARNING: speaker separation\" and \"diarize\" lines, "+
						"run `journal setup` again if a model is damaged, or set diarization = \"off\"", err, logPath))
			} else {
				add("speaker separation works", true, "")
			}
		}
	}

	_, err = d.LookPath("claude")
	add("claude", err == nil, "install Claude Code (https://claude.com/claude-code) so `claude` is on PATH")
	if err != nil {
		add("claude responds", false, "install Claude Code first")
	} else if err := d.ClaudeResponds(); err != nil {
		add("claude responds", false, fmt.Sprintf("%v: run `claude` and sign in", err))
	} else {
		add("claude responds", true, "")
	}

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

// speakerCheckTimeout bounds the "speaker separation works" check.
const speakerCheckTimeout = 2 * time.Minute

// speakerSeparationWorks runs the recording path end to end on about 6 s of
// two synthesized voices: `say` → AAC .m4a (as the recorder writes) →
// media.ToWav16k → the sherpa diarizer, which must find at least one turn.
func speakerSeparationWorks(segModel, embModel string) error {
	ctx, cancel := context.WithTimeout(context.Background(), speakerCheckTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "wj-doctor-speakers-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	run := media.ExecRunner
	var pcm []byte
	for i, l := range []struct{ voice, text string }{
		{"Samantha", "Good morning, shall we start with the release plan?"},
		{"Daniel", "Yes, the build is ready and the tests are passing."},
	} {
		aiff, wav := filepath.Join(dir, fmt.Sprintf("%d.aiff", i)), filepath.Join(dir, fmt.Sprintf("%d.wav", i))
		if _, err := run(ctx, "say", "-v", l.voice, "-o", aiff, l.text); err != nil {
			return err
		}
		if err := media.ToWav16k(ctx, run, aiff, wav); err != nil {
			return err
		}
		b, err := media.ReadWav16k(wav)
		if err != nil {
			return err
		}
		pcm = append(append(pcm, b...), make([]byte, 16000)...) // 0.5 s gap
	}
	mix, m4a, wav := filepath.Join(dir, "mix.wav"), filepath.Join(dir, "mix.m4a"), filepath.Join(dir, "mix-16k.wav")
	if err := media.WriteWav16k(mix, pcm); err != nil {
		return err
	}
	if _, err := run(ctx, "afconvert", "-f", "m4af", "-d", "aac", mix, m4a); err != nil {
		return err
	}
	if err := media.ToWav16k(ctx, run, m4a, wav); err != nil {
		return err
	}
	d, err := diarize.NewSherpaDiarizer(segModel, embModel)
	if err != nil {
		return err
	}
	defer d.Close()
	turns, _, err := d.Diarize(ctx, wav)
	if err != nil {
		return err
	}
	if len(turns) == 0 {
		return errors.New("diarizer found no speaker turns in a two-voice test recording")
	}
	return nil
}

// claudeProbeTimeout bounds the "claude responds" check.
const claudeProbeTimeout = 60 * time.Second

// claudeResponds asks claude for a trivial reply with no tools and no MCP
// servers; it fails when claude cannot run, is signed out or errors.
func claudeResponds(run media.Runner) error {
	ctx, cancel := context.WithTimeout(context.Background(), claudeProbeTimeout)
	defer cancel()
	out, err := run(ctx, "claude", "-p", "Reply with OK", "--output-format", "json", "--tools", "", "--strict-mcp-config")
	if err != nil {
		return err
	}
	var env struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("unexpected claude output: %.200s", strings.TrimSpace(string(out)))
	}
	if env.IsError {
		return fmt.Errorf("claude reported an error: %.200s", env.Result)
	}
	return nil
}
