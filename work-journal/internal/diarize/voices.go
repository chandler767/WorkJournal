package diarize

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Voices is the set of enrolled voice profiles stored as <slug>.json files
// in Dir. Embeddings never leave this directory.
type Voices struct {
	Dir       string
	Threshold float64
}

type profile struct {
	Name       string      `json:"name"`
	Embeddings [][]float32 `json:"embeddings"`
}

// Cosine returns the cosine similarity of a and b, or 0 when the lengths
// differ or either vector is empty or zero.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Match returns the enrolled voice most similar to emb. ok is true when the
// best score is at least Threshold. Unreadable profiles are ignored.
func (v *Voices) Match(emb []float32) (name string, score float64, ok bool) {
	profs, _ := v.load()
	best, bestName := math.Inf(-1), ""
	for _, p := range profs {
		for _, e := range p.Embeddings {
			if s := Cosine(emb, e); s > best {
				best, bestName = s, p.Name
			}
		}
	}
	if bestName == "" {
		return "", 0, false
	}
	return bestName, best, best >= v.Threshold
}

// Enroll appends emb to name's profile, creating it if needed.
func (v *Voices) Enroll(name string, emb []float32) error {
	path, err := v.path(name)
	if err != nil {
		return err
	}
	if len(emb) == 0 {
		return errors.New("voices: empty embedding")
	}
	p := profile{Name: name}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &p); err != nil {
			return fmt.Errorf("voices: parse %s: %w", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	p.Embeddings = append(p.Embeddings, emb)
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(v.Dir, ".voice-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// List returns the enrolled names, sorted.
func (v *Voices) List() ([]string, error) {
	profs, err := v.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(profs))
	for _, p := range profs {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names, nil
}

// Delete removes name's profile. A missing profile yields an error
// satisfying errors.Is(err, fs.ErrNotExist).
func (v *Voices) Delete(name string) error {
	path, err := v.path(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (v *Voices) path(name string) (string, error) {
	s := slug(name)
	if s == "" {
		return "", fmt.Errorf("voices: name %q has no usable characters", name)
	}
	return filepath.Join(v.Dir, s+".json"), nil
}

// load reads every profile in Dir. A missing Dir is empty. Malformed files
// are skipped.
func (v *Voices) load() ([]profile, error) {
	files, err := filepath.Glob(filepath.Join(v.Dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []profile
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p profile
		if json.Unmarshal(b, &p) != nil || p.Name == "" {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
