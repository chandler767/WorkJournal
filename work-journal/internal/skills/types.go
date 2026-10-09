package skills

// ActionItem is a commitment with its owner ("" when unknown).
type ActionItem struct {
	Text  string `json:"text"`
	Owner string `json:"owner"`
}

// TimelineEntry is one stretch of an hour: HH:MM start and end, the app and
// what was done.
type TimelineEntry struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	App      string `json:"app"`
	Activity string `json:"activity"`
}

// HourSummary is the summarize-hour output. Narrative and Timeline are absent
// (zero) in summaries stored before they were introduced.
type HourSummary struct {
	Narrative   string          `json:"narrative"`
	Timeline    []TimelineEntry `json:"timeline"`
	Notes       []string        `json:"notes"`
	MainFocus   string          `json:"main_focus"`
	ActionItems []ActionItem    `json:"action_items"`
}

// SegmentCategory is one categorize-time entry, keyed by the input index.
type SegmentCategory struct {
	Index      int     `json:"index"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
}

// Categorization is the categorize-time output.
type Categorization struct {
	Segments []SegmentCategory `json:"segments"`
}

// Participant is a speaker label with a proposed name ("" when unknown).
type Participant struct {
	Speaker    string  `json:"speaker"`
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
}

// MeetingSummary is the summarize-meeting output.
type MeetingSummary struct {
	Participants []Participant `json:"participants"`
	Summary      string        `json:"summary"`
	Decisions    []string      `json:"decisions"`
	ActionItems  []ActionItem  `json:"action_items"`
}

// Markdown is the close-day and weekly-rollup output.
type Markdown struct {
	Markdown string `json:"markdown"`
}

// DropRange is a privacy-filter decision to remove the input utterances
// From..To (indexes, inclusive).
type DropRange struct {
	From       int     `json:"from"`
	To         int     `json:"to"`
	Reason     string  `json:"reason"` // "bystander" or "personal_non_work"
	Confidence float64 `json:"confidence"`
}

// Redaction is an utterance's text with sensitive identifiers replaced by
// "[redacted]".
type Redaction struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

// PrivacyResult is the privacy-filter output.
type PrivacyResult struct {
	Drop   []DropRange `json:"drop"`
	Redact []Redaction `json:"redact"`
}
