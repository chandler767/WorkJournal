package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/media"
	"workjournal/internal/skills"
)

// The privacy filter keeps ambient audio out of everything derived from an
// hour: the privacy-filter skill marks bystander conversations (the user not
// taking part, not in a meeting) and the user's personal non-work
// conversations with non-coworkers for removal, and returns the text of
// utterances with sensitive identifiers redacted. Removed speech never
// reaches the derived doc, the search index, meeting detection or any other
// skill; the doc keeps only the removed spans' times (RemovedSpan).
//
// Decisions are kept per utterance in cache/<day>/privacy.json (outside
// derived/), so the in-progress hour's repeated passes and the final pass
// send only utterances without a decision, plus up to privacyContext of
// earlier, already decided speech as context. A decision keeps the skill's
// confidence: privacy_drop_confidence and redact_identifiers apply when the
// decisions are used.

const privacySkill = "privacy-filter"

// privacyContext is how much decided speech before an undecided utterance
// is sent along as context.
const privacyContext = 2 * time.Minute

// redactedMark replaces each identifier in a redacted text (any case).
const redactedMark = "[redacted]"

// RemovedSpan is a stretch of speech the privacy filter removed; it holds no
// text.
type RemovedSpan struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Reason string    `json:"reason"` // "bystander" or "personal_non_work"
}

// Skill input shapes, matching the "Input" section of privacy-filter's
// SKILL.md.
type (
	privacyInput struct {
		Day      string           `json:"day"`
		Hour     int              `json:"hour"`
		Meetings []privacyMeeting `json:"meetings"`
		// KnownPeople are the enrolled voices' names (coworkers named on
		// meeting pages); UserVoiceEnrolled says whether the user's own
		// voice is, i.e. whether "You" can appear in an in-person meeting.
		KnownPeople       []string      `json:"known_people"`
		UserVoiceEnrolled bool          `json:"user_voice_enrolled"`
		Utterances        []privacyLine `json:"utterances"`
	}
	privacyMeeting struct {
		ID           string   `json:"id"`
		App          string   `json:"app"`
		Start        string   `json:"start"` // HH:MM:SS
		End          string   `json:"end"`
		Participants []string `json:"participants"`
	}
	privacyLine struct {
		Index   int    `json:"index"`
		Time    string `json:"time"` // HH:MM:SS
		Track   string `json:"track"`
		Speaker string `json:"speaker"`
		Text    string `json:"text"`
		Context bool   `json:"context,omitempty"`
	}
)

// checkedPrivacy rejects indexes outside the input, reversed ranges and
// redactions that do not mark what they redacted.
type checkedPrivacy struct {
	skills.PrivacyResult
	lines int
}

func (c *checkedPrivacy) Validate() error {
	for i, d := range c.Drop {
		if d.From < 0 || d.To >= c.lines || d.From > d.To {
			return fmt.Errorf("drop[%d] %d-%d is not a range of the %d utterances", i, d.From, d.To, c.lines)
		}
	}
	for i, r := range c.Redact {
		if r.Index < 0 || r.Index >= c.lines {
			return fmt.Errorf("redact[%d] index %d is not one of the %d utterances", i, r.Index, c.lines)
		}
		if !strings.Contains(strings.ToLower(r.Text), redactedMark) {
			return fmt.Errorf("redact[%d] text has no %s", i, redactedMark)
		}
	}
	return nil
}

// privacyDecision is the filter's verdict on one utterance.
type privacyDecision struct {
	Drop       bool    `json:"drop,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Redacted   string  `json:"redacted,omitempty"` // the text with identifiers redacted
}

type privacyCache struct {
	Version int                        `json:"version"`
	Lines   map[string]privacyDecision `json:"lines"` // by privacyKey
}

// privacyKey identifies an utterance across passes: whisper output is cached
// per chunk, so track, start and text are stable; speaker labels are not.
func privacyKey(u media.Utterance) string {
	h := sha256.Sum256([]byte(u.Text))
	return u.Track + "|" + u.Start.UTC().Format(time.RFC3339Nano) + "|" + hex.EncodeToString(h[:8])
}

func (p *Pipeline) privacyCachePath(day string) string {
	return filepath.Join(p.Paths.CacheDay(day), "privacy.json")
}

func (p *Pipeline) loadPrivacyCache(day string) *privacyCache {
	c := &privacyCache{Version: cacheVersion, Lines: map[string]privacyDecision{}}
	b, err := os.ReadFile(p.privacyCachePath(day))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			p.logf("process: privacy cache %s: %v", day, err)
		}
		return c
	}
	var got privacyCache
	if err := json.Unmarshal(b, &got); err != nil || got.Version != cacheVersion || got.Lines == nil {
		p.logf("process: privacy cache %s unusable; asking again", day)
		return c
	}
	return &got
}

// privacyOn reports whether speech goes through the filter.
func (p *Pipeline) privacyOn() bool {
	return p.Cfg.PrivacyFilter && p.Privacy != nil
}

// decidePrivacy returns a decision for every utterance of us (the hour's
// audible speech, in time order), running the skill on those without a
// cached one. A skill failure is returned as is: skills.ErrUnavailable is
// transient (nothing is written, the hour is retried); on
// skills.ErrInvalidOutput the final pass withholds the hour's speech.
func (p *Pipeline) decidePrivacy(ctx context.Context, day string, hour int, us []media.Utterance, meetings []activity.Meeting) (map[string]privacyDecision, error) {
	cache := p.loadPrivacyCache(day)
	var undecided []time.Time
	for _, u := range us {
		if _, ok := cache.Lines[privacyKey(u)]; !ok {
			undecided = append(undecided, u.Start)
		}
	}
	if len(undecided) == 0 {
		return cache.Lines, nil
	}
	in := privacyInput{Day: day, Hour: hour, Meetings: p.privacyMeetings(us, meetings), Utterances: []privacyLine{}}
	in.KnownPeople, in.UserVoiceEnrolled = p.knownPeople()
	var sent []media.Utterance // sent[i] is line i
	for _, u := range us {
		_, decided := cache.Lines[privacyKey(u)]
		if decided && !slices.ContainsFunc(undecided, func(t time.Time) bool {
			return !u.Start.After(t) && !u.Start.Before(t.Add(-privacyContext))
		}) {
			continue
		}
		in.Utterances = append(in.Utterances, privacyLine{Index: len(sent), Time: u.Start.Format("15:04:05"),
			Track: u.Track, Speaker: speaker(u), Text: u.Text, Context: decided})
		sent = append(sent, u)
	}
	out := &checkedPrivacy{lines: len(sent)}
	if err := p.runPrivacy(ctx, day, hour, in, out); err != nil {
		return nil, err
	}
	fresh := map[int]privacyDecision{}
	for i, l := range in.Utterances {
		if !l.Context {
			fresh[i] = privacyDecision{}
		}
	}
	for _, d := range out.Drop {
		for i := d.From; i <= d.To; i++ {
			dec, ok := fresh[i]
			if !ok {
				// A context line: a drop range over it upgrades its earlier
				// decision (a keep becomes a drop, never the reverse).
				dec = cache.Lines[privacyKey(sent[i])]
			}
			if !dec.Drop || d.Confidence > dec.Confidence {
				dec.Drop, dec.Reason, dec.Confidence = true, d.Reason, d.Confidence
			}
			if ok {
				fresh[i] = dec
			} else {
				cache.Lines[privacyKey(sent[i])] = dec
			}
		}
	}
	for _, r := range out.Redact {
		if dec, ok := fresh[r.Index]; ok {
			dec.Redacted = r.Text
			fresh[r.Index] = dec
		}
	}
	for i, dec := range fresh {
		cache.Lines[privacyKey(sent[i])] = dec
	}
	if err := writePrivateJSON(p.privacyCachePath(day), cache); err != nil {
		// Only costs asking again next pass.
		p.logf("process: privacy cache %s: %v", day, err)
	}
	return cache.Lines, nil
}

// runPrivacy writes the skill input next to the other skill inputs (the
// only place Claude may read), runs the skill and removes the input: it
// holds the speech that may be removed.
func (p *Pipeline) runPrivacy(ctx context.Context, day string, hour int, in privacyInput, out *checkedPrivacy) error {
	b, err := skills.MarshalInput(in)
	if err != nil {
		return err
	}
	dir := filepath.Join(filepath.Dir(p.Paths.DerivedHour(day, hour)), ".inputs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("%02d-%s.json", hour, privacySkill))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write skill input: %w", err)
	}
	defer os.Remove(path)
	if err := p.Privacy.Run(ctx, privacySkill, path, out); err != nil {
		return fmt.Errorf("privacy filter: %w", err)
	}
	return nil
}

// privacyMeetings describes meetings for the skill; participants are the
// speaker labels heard in each and the names stored for it.
func (p *Pipeline) privacyMeetings(us []media.Utterance, meetings []activity.Meeting) []privacyMeeting {
	out := []privacyMeeting{}
	for _, m := range meetings {
		pm := privacyMeeting{ID: m.ID, App: m.App, Start: m.Start.Format("15:04:05"),
			End: m.End.Format("15:04:05"), Participants: []string{}}
		add := func(s string) {
			if s != "" && s != "others" && s != unknownSpeaker && !slices.Contains(pm.Participants, s) {
				pm.Participants = append(pm.Participants, s)
			}
		}
		for _, u := range us {
			if inMeetingSpan(m, u) {
				add(speaker(u))
			}
		}
		if parts, err := p.Store.Participants(m.ID); err != nil {
			p.logf("process: participants of %s: %v", m.ID, err)
		} else {
			for _, r := range parts {
				add(r.Name)
			}
		}
		out = append(out, pm)
	}
	return out
}

// knownPeople returns the enrolled voices' names other than the user's, and
// whether the user's own voice ("me") is enrolled.
func (p *Pipeline) knownPeople() ([]string, bool) {
	out := []string{}
	if p.Voices == nil || p.diarizer() == nil {
		return out, false
	}
	names, err := p.Voices.List()
	if err != nil {
		p.logf("process: voice profiles: %v", err)
		return out, false
	}
	me := false
	for _, n := range names {
		if n == "me" {
			me = true
			continue
		}
		out = append(out, n)
	}
	return out, me
}

// applyPrivacy returns us without the utterances dropped with at least
// privacy_drop_confidence (redacted when redact_identifiers is on), and the
// removed spans. An utterance without a decision is kept.
func (p *Pipeline) applyPrivacy(us []media.Utterance, dec map[string]privacyDecision) ([]media.Utterance, []RemovedSpan) {
	var kept []media.Utterance
	dropped := map[int]string{}
	for i, u := range us {
		d := dec[privacyKey(u)]
		if p.dropped(d) {
			dropped[i] = d.Reason
			continue
		}
		if p.Cfg.RedactIdentifiers && d.Redacted != "" {
			u.Text = d.Redacted
		}
		kept = append(kept, u)
	}
	return kept, removedSpans(us, dropped)
}

func (p *Pipeline) dropped(d privacyDecision) bool {
	return d.Drop && d.Confidence >= p.Cfg.PrivacyDropConfidence
}

// withoutDropped returns the utterances of us (unlabelled speech, for
// meeting detection) that the decisions do not drop.
func (p *Pipeline) withoutDropped(us []media.Utterance, dec map[string]privacyDecision) []media.Utterance {
	return slices.DeleteFunc(slices.Clone(us), func(u media.Utterance) bool { return p.dropped(dec[privacyKey(u)]) })
}

// removedSpans merges runs of consecutive dropped utterances of us (by
// index, with their reasons) into spans; a kept utterance or a change of
// reason ends a run.
func removedSpans(us []media.Utterance, dropped map[int]string) []RemovedSpan {
	var out []RemovedSpan
	open := false
	for i, u := range us {
		reason, ok := dropped[i]
		switch {
		case !ok:
			open = false
		case open && out[len(out)-1].Reason == reason:
			if u.End.After(out[len(out)-1].End) {
				out[len(out)-1].End = u.End
			}
		default:
			out = append(out, RemovedSpan{Start: u.Start, End: u.End, Reason: reason})
			open = true
		}
	}
	return out
}

// hourAudio is what build gathered about an hour, for filterHour.
type hourAudio struct {
	day        string
	hour       int
	start, end time.Time
	cut        time.Time
	events     []activity.Event
	segs       []activity.Segment
	audio      []audioChunk
	speech     []media.Utterance  // unlabelled audible speech
	detected   []activity.Meeting // meetings detected from speech
	wavs       *wavDir
}

// hearing is the hour's labelled speech and meetings.
type hearing struct {
	lab      *labelled
	utts     []media.Utterance
	meetings []activity.Meeting
	removed  []RemovedSpan
	withheld bool
}

// filterHour runs the privacy filter on h's speech and applies it. When
// anything is removed, meetings are detected again without it (a bystander
// conversation is not an in-person meeting) and, if they changed, the audio
// is labelled again for them. On invalid filter output the final pass
// withholds all speech (retrying would fail the same way); a partial pass,
// and any other failure, returns the error and nothing is written.
func (p *Pipeline) filterHour(ctx context.Context, a hourAudio, h *hearing, partial bool) (*hearing, error) {
	relabel := func(speech []media.Utterance) (*hearing, error) {
		again, err := p.detectMeetings(a.day, a.hour, a.start, a.cut, a.events, speech)
		if err != nil {
			return nil, err
		}
		audio := a.audio
		if speech == nil {
			audio = nil
		}
		if sameMeetings(again, a.detected) && speech != nil {
			return h, nil
		}
		lab, utts, meetings, err := p.labelHour(ctx, a.day, a.hour, a.start, a.end, audio, again, a.segs, a.wavs)
		if err != nil {
			return nil, err
		}
		return &hearing{lab: lab, utts: utts, meetings: meetings}, nil
	}
	dec, err := p.decidePrivacy(ctx, a.day, a.hour, h.utts, h.meetings)
	if errors.Is(err, skills.ErrInvalidOutput) && !partial {
		p.logf("process: hour %s %02d: %v; its speech is withheld and the hour needs review", a.day, a.hour, err)
		out, err := relabel(nil)
		if err != nil {
			return nil, err
		}
		out.utts, out.withheld = nil, true
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	kept, removed := p.applyPrivacy(h.utts, dec)
	if len(removed) == 0 {
		return &hearing{lab: h.lab, utts: kept, meetings: h.meetings}, nil
	}
	out, err := relabel(p.withoutDropped(a.speech, dec))
	if err != nil {
		return nil, err
	}
	out.utts, out.removed = p.applyPrivacy(out.utts, dec)
	return out, nil
}

// ForgetPrivacy removes the cached decisions on the hour's utterances, so
// the next pass asks again (Reprocess: a wrong removal can be undone).
func (p *Pipeline) ForgetPrivacy(day string, hour int) error {
	if _, err := os.Stat(p.privacyCachePath(day)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	start, end, err := hourBounds(p.Paths, day, hour)
	if err != nil {
		return err
	}
	c := p.loadPrivacyCache(day)
	for k := range c.Lines {
		parts := strings.SplitN(k, "|", 3)
		if len(parts) != 3 {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, parts[1]); err == nil && !t.Before(start) && t.Before(end) {
			delete(c.Lines, k)
		}
	}
	return writePrivateJSON(p.privacyCachePath(day), c)
}
