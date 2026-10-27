package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/notes"
	"workjournal/internal/store"
)

// runNote adds a note (`journal note add`) or lists a day's notes (`journal
// note list`). It opens only the database, beside a running serve, which
// reprocesses what the note covers on its next tick.
func runNote(cfg config.Config, action string, d notes.Draft, file string, stdout io.Writer) error {
	if d.Day == "" {
		return errors.New("--date is required")
	}
	p := config.Paths{Root: cfg.DataDir}
	if err := p.Ensure(); err != nil {
		return err
	}
	st, err := store.Open(p.DB())
	if err != nil {
		return err
	}
	defer st.Close()
	if action == "list" {
		return listNotes(st, d.Day, stdout)
	}
	switch {
	case file != "" && d.Text != "":
		return errors.New("give --text or --file, not both")
	case file != "":
		if d.Text, err = notes.ReadFile(file); err != nil {
			return err
		}
	case d.Text == "":
		return errors.New("give --text or --file")
	}
	n, err := notes.Add(st, d, store.SourceCLI, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "saved %s note %d on %s (%s)\n", n.Kind, n.ID, n.Day, notes.Range(n))
	if n.Kind == store.NoteCorrection && notes.RawGone(p, n.Day) {
		fmt.Fprintln(stdout, "the day's raw recordings were already removed, so this correction can change the summaries but not the transcript")
	}
	fmt.Fprintln(stdout, "journal serve applies it on its next tick")
	return nil
}

func listNotes(st *store.Store, day string, stdout io.Writer) error {
	ns, err := st.NotesOn(day)
	if err != nil {
		return err
	}
	if len(ns) == 0 {
		fmt.Fprintf(stdout, "no notes on %s\n", day)
	}
	for _, n := range ns {
		fmt.Fprintf(stdout, "%d  %s  %s  (%s)\n  %s\n", n.ID, n.Kind, notes.Range(n), n.Source, n.Text)
	}
	return nil
}
