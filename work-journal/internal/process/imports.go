package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

// An imported recording is audio recorded away from the Mac (a phone at a
// meetup) and handed to the journal by `journal import` or the phone upload
// page, with a start time and the user's own note (title and context). It
// lives in its own folder, imports/<id>/ (the audio and note.json), apart
// from raw/, so the recorder's chunks and hour processing never see it.
//
// Each import is one in-person meeting from its start to start plus the
// audio's duration, flagged imported (store.ImportRecord). Its id is the
// meeting's id: <day>-<HHMM>-imported, on the clock the start was given in.
// ProcessImports runs it through the steps of recorded meeting audio:
// whisper, diarization, voice matching, the privacy filter and identifier
// redaction, then summarize-meeting with the user's note. The labelled
// transcript is derived/imports/<id>.json (an HourDoc of the hour it starts
// in); the whisper and diarizer output is cached in cache/imports/<id>.json.

// MaxImportBytes caps an imported audio file.
const MaxImportBytes = 1 << 30

// Limits on the user's note, in characters.
const (
	MaxImportTitle   = 200
	MaxImportContext = 5000
)

// ImportExts are the audio file types an import accepts: what afconvert
// reads, without video.
var ImportExts = []string{".m4a", ".mp3", ".wav", ".caf", ".aac", ".aif", ".aiff"}

// importTitle names an imported meeting the user gave no title.
const importTitle = "Imported recording"

// importNoteFile is the sidecar holding an import's note, in its folder.
const importNoteFile = "note.json"

// errUnreadable marks an imported file afconvert cannot read; trying again
// would fail the same way.
var errUnreadable = errors.New("cannot read the audio")

// ImportNote is an import's sidecar: the user's note and where the audio is.
type ImportNote struct {
	Title    string    `json:"title,omitempty"`
	Context  string    `json:"context,omitempty"`
	Start    time.Time `json:"start"`
	File     string    `json:"file"`                    // the audio's name in the folder
	Original string    `json:"original_name,omitempty"` // the name it was imported under
	Imported time.Time `json:"imported_at"`
}

// WriteImportNote writes n as dir's sidecar.
func WriteImportNote(dir string, n ImportNote) error {
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, importNoteFile), b, 0o600)
}

// ReadImportNote reads dir's sidecar.
func ReadImportNote(dir string) (ImportNote, error) {
	var n ImportNote
	b, err := os.ReadFile(filepath.Join(dir, importNoteFile))
	if err != nil {
		return n, err
	}
	if err := json.Unmarshal(b, &n); err != nil {
		return n, fmt.Errorf("import note %s: %w", dir, err)
	}
	return n, nil
}

// CheckImportName rejects a file name whose extension is not one of
// ImportExts.
func CheckImportName(name string) error {
	if !slices.Contains(ImportExts, strings.ToLower(filepath.Ext(name))) {
		return fmt.Errorf("%q is not a supported audio file: use %s", filepath.Base(name), strings.Join(ImportExts, ", "))
	}
	return nil
}

// ImportRequest is one recording to import.
type ImportRequest struct {
	Path    string // the audio file
	Name    string // the name it was uploaded under; "" means Path's
	Start   time.Time
	Title   string
	Context string
	Move    bool // rename Path into the import's folder instead of copying it
}

// Import checks req, stores the audio and its note in a new folder under
// imports/, and records the meeting (from Start to Start until processing
// finds the duration) with a pending import. Nothing is recorded when it
// fails.
func Import(paths config.Paths, st *store.Store, req ImportRequest, now time.Time) (store.MeetingRecord, error) {
	name := req.Name
	if name == "" {
		name = filepath.Base(req.Path)
	}
	if err := CheckImportName(name); err != nil {
		return store.MeetingRecord{}, err
	}
	if req.Start.IsZero() {
		return store.MeetingRecord{}, errors.New("the start time is required")
	}
	title, note := strings.TrimSpace(req.Title), strings.TrimSpace(req.Context)
	if n := utf8.RuneCountInString(title); n > MaxImportTitle {
		return store.MeetingRecord{}, fmt.Errorf("the title is %d characters; the limit is %d", n, MaxImportTitle)
	}
	if n := utf8.RuneCountInString(note); n > MaxImportContext {
		return store.MeetingRecord{}, fmt.Errorf("the context is %d characters; the limit is %d", n, MaxImportContext)
	}
	fi, err := os.Stat(req.Path)
	if err != nil {
		return store.MeetingRecord{}, err
	}
	if !fi.Mode().IsRegular() {
		return store.MeetingRecord{}, fmt.Errorf("%s is not a regular file", req.Path)
	}
	if fi.Size() > MaxImportBytes {
		return store.MeetingRecord{}, fmt.Errorf("the file is %d MB; the limit is 1 GB", fi.Size()>>20)
	}
	if err := os.MkdirAll(paths.Imports(), 0o700); err != nil {
		return store.MeetingRecord{}, err
	}
	id, dir, err := newImportDir(paths, st, req.Start)
	if err != nil {
		return store.MeetingRecord{}, err
	}
	audio := "audio" + strings.ToLower(filepath.Ext(name))
	n := ImportNote{Title: title, Context: note, Start: req.Start, File: audio, Original: filepath.Base(name), Imported: now}
	err = placeFile(req.Path, filepath.Join(dir, audio), req.Move)
	if err == nil {
		err = WriteImportNote(dir, n)
	}
	if err != nil {
		os.RemoveAll(dir)
		return store.MeetingRecord{}, err
	}
	if title == "" {
		title = importTitle
	}
	m := store.MeetingRecord{ID: id, Day: config.DayKey(req.Start), Start: req.Start, End: req.Start, App: activity.InPersonApp, Title: title}
	if err := st.UpsertMeeting(m); err != nil {
		os.RemoveAll(dir)
		return store.MeetingRecord{}, err
	}
	// The import row comes last: ProcessImports only sees a complete import.
	if err := st.UpsertImport(store.ImportRecord{ID: id, Status: store.StatusPending, UpdatedAt: now}); err != nil {
		os.RemoveAll(dir)
		st.DeleteMeeting(id)
		return store.MeetingRecord{}, err
	}
	m.Imported, m.ImportStatus = true, store.StatusPending
	return m, nil
}

// newImportDir creates the folder of a new import starting at start and
// returns its id: <day>-<HHMM>-imported, with -2, -3, ... appended while the
// id is taken by a meeting or a folder.
func newImportDir(paths config.Paths, st *store.Store, start time.Time) (string, string, error) {
	base := fmt.Sprintf("%s-%s-imported", config.DayKey(start), start.Format("1504"))
	for n := 1; n < 1000; n++ {
		id := base
		if n > 1 {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		if _, taken, err := st.Meeting(id); err != nil {
			return "", "", err
		} else if taken {
			continue
		}
		dir := paths.Import(id)
		if err := os.Mkdir(dir, 0o700); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", "", err
		}
		return id, dir, nil
	}
	return "", "", fmt.Errorf("too many imports starting at %s", start.Format(time.RFC3339))
}

// placeFile moves (rename) or copies src to dst. A copy stops with an
// error past MaxImportBytes, in case src grew after it was checked.
func placeFile(src, dst string, move bool) error {
	if move {
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, MaxImportBytes+1))
	if err == nil && n > MaxImportBytes {
		err = errors.New("the file is over the 1 GB limit")
	}
	if err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if move {
		os.Remove(src)
	}
	return nil
}

// ImportSummarizer summarizes an imported recording's meeting (see
// SkillSummarizer.SummarizeImport).
type ImportSummarizer interface {
	SummarizeImport(ctx context.Context, doc *HourDoc, m activity.Meeting, note ImportNote) (skills.MeetingSummary, error)
}

// importEntry is the cached processing of an imported file.
type importEntry struct {
	chunkEntry
	Seconds float64 `json:"seconds"` // the audio's duration; 0 when unknown
}

func (p *Pipeline) loadImportEntry(id string, key fileKey) (*importEntry, bool) {
	b, err := os.ReadFile(p.Paths.ImportCache(id))
	if err != nil {
		return nil, false
	}
	var e importEntry
	if err := json.Unmarshal(b, &e); err != nil || e.Version != cacheVersion || e.Key != key || e.Model != p.Cfg.WhisperModel {
		return nil, false
	}
	return &e, true
}

// importRetry is how long a failed import waits before it is tried again.
const importRetry = time.Hour

// ProcessImports processes every pending import, oldest first. A failure is
// recorded on the import, so the meeting's dashboard entry shows it: audio
// afconvert cannot read leaves it needs_review (trying again would fail the
// same way), anything else leaves it pending, to be tried again once
// importRetry has passed. The audio is never removed. The joined errors are
// returned for logging.
func (p *Pipeline) ProcessImports(ctx context.Context) error {
	recs, err := p.Store.ImportsByStatus(store.StatusPending)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range recs {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if r.Error != "" && p.now().Sub(r.UpdatedAt) < importRetry {
			continue
		}
		status, msg, err := p.processImport(ctx, r.ID)
		if err != nil {
			if ctx.Err() != nil {
				return errors.Join(append(errs, err)...)
			}
			status, msg = store.StatusPending, err.Error()
			if errors.Is(err, errUnreadable) {
				status = store.StatusNeedsReview
			}
			errs = append(errs, fmt.Errorf("import %s: %w", r.ID, err))
		}
		if err := p.Store.UpsertImport(store.ImportRecord{ID: r.ID, Status: status, Error: msg, UpdatedAt: p.now()}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// processImport runs import id through the pipeline and returns its new
// status with a message for the user ("" when all went well). An error
// leaves the import as it was.
func (p *Pipeline) processImport(ctx context.Context, id string) (status, msg string, err error) {
	dir := p.Paths.Import(id)
	note, err := ReadImportNote(dir)
	if err != nil {
		return "", "", err
	}
	audio := filepath.Join(dir, note.File)
	key, err := statKey(audio)
	if err != nil {
		return "", "", fmt.Errorf("the audio file is missing: %w", err)
	}
	rec, ok, err := p.Store.Meeting(id)
	if err != nil {
		return "", "", err
	}
	if !ok {
		title := note.Title
		if title == "" {
			title = importTitle
		}
		rec = store.MeetingRecord{ID: id, Day: config.DayKey(note.Start), Start: note.Start, App: activity.InPersonApp, Title: title}
	}
	start := note.Start
	c := chunk{path: audio, start: start, track: "mic"}

	d := p.diarizer()
	var wavs wavDir
	defer wavs.close()
	e, cached := p.loadImportEntry(id, key)
	needText := !cached
	needTurns := d != nil && (!cached || !e.Diarized)
	if needText || needTurns {
		if err := wavs.ensure(); err != nil {
			return "", "", err
		}
		wav, normErr, err := wavs.convert(ctx, p, c)
		if err != nil {
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			return "", "", fmt.Errorf("%w: %v", errUnreadable, err)
		}
		if needText {
			us, err := p.Transcriber.Transcribe(ctx, wav, start, "mic")
			if err != nil {
				return "", "", fmt.Errorf("transcribe: %w", err)
			}
			e = &importEntry{chunkEntry: chunkEntry{Version: cacheVersion, Key: key, Model: p.Cfg.WhisperModel, Utterances: us}}
			if dur, err := media.Wav16kDuration(wav); err == nil {
				e.Seconds = dur.Seconds()
			}
		}
		if needTurns && normErr != nil {
			p.logf("process: import %s: %v; speakers unknown", id, normErr)
		} else if needTurns {
			p.diarizeChunk(ctx, d, wav, id, &e.chunkEntry, &wavs)
		}
		if err := writePrivateJSON(p.Paths.ImportCache(id), e); err != nil {
			p.logf("process: import cache %s: %v", id, err)
		}
	}
	us := media.DropHallucinations(e.Utterances)

	end := start.Add(secs(e.Seconds))
	for _, u := range e.Utterances {
		if u.End.After(end) {
			end = u.End
		}
	}
	rec.Start, rec.End = start, end
	if err := p.Store.UpsertMeeting(rec); err != nil {
		return "", "", err
	}
	m := activity.Meeting{ID: id, Start: start, End: end, App: activity.InPersonApp, Title: rec.Title}

	// Speakers: the whole recording is one in-person meeting on the mic.
	// Without a diarization its speech stays "me", as an in-person
	// meeting's mic speech does when it cannot be diarized.
	if d != nil && e.Diarized {
		embs := map[string][]float32{}
		sc := p.clusterScope([]diarChunk{{c, &e.chunkEntry}}, meetingWithin(m), embs)
		sp := &speakerSet{meetings: []activity.Meeting{m}, scopes: []*speakerScope{sc}, hour: sc, embs: embs}
		sp.label(c, &e.chunkEntry, us, nil, unknownSpeaker)
	}

	doc := &HourDoc{Day: rec.Day, Hour: start.Hour(), Status: store.StatusTranscribed,
		Transcript: us, Meetings: []activity.Meeting{m}, EndedMeetings: []string{id}}
	if p.privacyOn() && len(us) > 0 {
		kept, removed, err := p.filterImport(ctx, m, us)
		switch {
		case errors.Is(err, skills.ErrInvalidOutput):
			p.logf("process: import %s: %v; its speech is withheld", id, err)
			doc.Transcript, doc.SpeechWithheld, doc.Status = nil, true, store.StatusNeedsReview
		case err != nil:
			return "", "", err
		default:
			doc.Transcript, doc.Removed = kept, removed
		}
	}
	if err := doc.Save(p.Paths.ImportDoc(id)); err != nil {
		return "", "", fmt.Errorf("save derived doc: %w", err)
	}
	texts := importText(id, doc)
	if err := p.Store.ReplaceImportText(id, texts); err != nil {
		return "", "", fmt.Errorf("index text: %w", err)
	}
	if doc.SpeechWithheld {
		return store.StatusNeedsReview, "The privacy check gave invalid output, so the recording's speech is withheld.", nil
	}
	s, ok := p.Summarize.(ImportSummarizer)
	if !ok {
		return store.StatusDone, "", nil
	}
	ms, err := s.SummarizeImport(ctx, doc, m, note)
	if errors.Is(err, skills.ErrInvalidOutput) {
		p.logf("process: import %s: %v", id, err)
		return store.StatusNeedsReview, "The meeting summary was invalid; the transcript is kept.", nil
	}
	if err != nil {
		return "", "", err
	}
	texts = append(texts, store.TextDoc{Kind: "meeting", Day: rec.Day, Hour: start.Hour(), TS: end, Text: meetingText(ms), Ref: "meeting/" + id})
	if err := p.Store.ReplaceImportText(id, texts); err != nil {
		return "", "", fmt.Errorf("index text: %w", err)
	}
	return store.StatusDone, "", nil
}

// filterImport runs the privacy filter over an import's speech one recorded
// hour at a time, as hour processing does, and applies it.
func (p *Pipeline) filterImport(ctx context.Context, m activity.Meeting, us []media.Utterance) ([]media.Utterance, []RemovedSpan, error) {
	dec := map[string]privacyDecision{}
	for i := 0; i < len(us); {
		day, hour := config.DayKey(us[i].Start), us[i].Start.Hour()
		j := i
		for j < len(us) && sameHour(us[j].Start, day, hour) {
			j++
		}
		got, err := p.decidePrivacy(ctx, day, hour, us[i:j], []activity.Meeting{m})
		if err != nil {
			return nil, nil, err
		}
		for _, u := range us[i:j] {
			k := privacyKey(u)
			dec[k] = got[k]
		}
		i = j
	}
	kept, removed := p.applyPrivacy(us, dec)
	return kept, removed, nil
}

// importText is an import's searchable transcript; it links to the meeting,
// since no hour page shows it.
func importText(id string, d *HourDoc) []store.TextDoc {
	var out []store.TextDoc
	for _, u := range d.Transcript {
		out = append(out, store.TextDoc{Kind: "transcript", Day: config.DayKey(u.Start), Hour: u.Start.Hour(), TS: u.Start,
			Text: speaker(u) + ": " + u.Text, Ref: "meeting/" + id})
	}
	return out
}

// MeetingDocs returns the derived docs holding meeting m's speech: an
// imported recording's own doc, else those of the recorded hours it touches
// that have one.
func MeetingDocs(paths config.Paths, m store.MeetingRecord) []*HourDoc {
	if m.Imported {
		if d, err := Load(paths.ImportDoc(m.ID)); err == nil {
			return []*HourDoc{d}
		}
		return nil
	}
	var out []*HourDoc
	for _, t := range HourStarts(m.Start, m.End, m.Start.Location()) {
		if d, err := Load(paths.DerivedHour(config.DayKey(t), t.Hour())); err == nil {
			out = append(out, d)
		}
	}
	return out
}
