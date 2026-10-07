package skills

// ActionItem is a commitment with its owner ("" when unknown).
type ActionItem struct {
	Text  string `json:"text"`
	Owner string `json:"owner"`
}

// HourSummary is the summarize-hour output.
type HourSummary struct {
	Notes       []string     `json:"notes"`
	MainFocus   string       `json:"main_focus"`
	ActionItems []ActionItem `json:"action_items"`
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
