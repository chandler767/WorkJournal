package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
)

// fileStamp is a cheap change marker for a file: mtime and size, or "-".
func fileStamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ":" + strconv.FormatInt(fi.Size(), 10)
}

func shortHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%s\x1f", p)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// dayRev fingerprints the day page: database rows, the journal file, the
// recorder state and the banners' KV inputs.
func (s *Server) dayRev(day string) (string, error) {
	db, err := s.Store.DayRev(day)
	if err != nil {
		return "", err
	}
	_, _, state := s.recorderState()
	disk, _, _ := s.Store.GetKV("disk_state")
	return shortHash(db, fileStamp(s.Paths.DailyJournal(day)), state, disk, boolStr(s.SkillsMissing)), nil
}

// hourRev fingerprints the hour page: database rows and the derived doc.
func (s *Server) hourRev(day string, hour int) (string, error) {
	db, err := s.Store.HourRev(day, hour)
	if err != nil {
		return "", err
	}
	return shortHash(db, fileStamp(s.Paths.DerivedHour(day, hour))), nil
}

func boolStr(b bool) string { return strconv.FormatBool(b) }

func writeRev(w http.ResponseWriter, rev string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"rev": rev})
}

func (s *Server) handleRevDay(w http.ResponseWriter, r *http.Request) {
	day, _, ok := parseDate(r.PathValue("date"))
	if !ok {
		http.Error(w, "bad date", http.StatusBadRequest)
		return
	}
	rev, err := s.dayRev(day)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeRev(w, rev)
}

func (s *Server) handleRevHour(w http.ResponseWriter, r *http.Request) {
	day, _, ok := parseDate(r.PathValue("date"))
	hour, ok2 := parseHour(r.PathValue("hh"))
	if !ok || !ok2 {
		http.Error(w, "bad date or hour", http.StatusBadRequest)
		return
	}
	rev, err := s.hourRev(day, hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeRev(w, rev)
}
