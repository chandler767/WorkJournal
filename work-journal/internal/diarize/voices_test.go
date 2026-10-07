package diarize

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCosine(t *testing.T) {
	cases := []struct {
		a, b []float32
		want float64
	}{
		{[]float32{1, 0, 0}, []float32{1, 0, 0}, 1},
		{[]float32{1, 0, 0}, []float32{0, 1, 0}, 0},
		{[]float32{1, 0}, []float32{-1, 0}, -1},
		{[]float32{3, 4}, []float32{6, 8}, 1},
		{[]float32{1, 0}, []float32{1, 0, 0}, 0}, // length mismatch
		{[]float32{0, 0}, []float32{1, 0}, 0},    // zero vector
		{nil, nil, 0},
	}
	for _, c := range cases {
		if got := Cosine(c.a, c.b); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("Cosine(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestVoicesEnrollMatchDelete(t *testing.T) {
	v := &Voices{Dir: filepath.Join(t.TempDir(), "voices"), Threshold: 0.75}

	// Empty/missing dir: nothing matches, nothing listed.
	if _, _, ok := v.Match([]float32{1, 0, 0}); ok {
		t.Fatal("match against empty dir")
	}
	if names, err := v.List(); err != nil || len(names) != 0 {
		t.Fatalf("List on missing dir = %v, %v", names, err)
	}

	if err := v.Enroll("Priya Shah", []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	name, score, ok := v.Match([]float32{0.9, 0.1, 0})
	if !ok || name != "Priya Shah" || score < 0.75 {
		t.Fatalf("Match = %q %v %v", name, score, ok)
	}
	if _, _, ok := v.Match([]float32{0, 1, 0}); ok {
		t.Fatal("[0,1,0] must not match")
	}

	// Enroll appends to the same profile file.
	if err := v.Enroll("Priya Shah", []float32{0, 1, 0}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(v.Dir, "priya-shah.json"))
	if err != nil {
		t.Fatal(err)
	}
	var prof struct {
		Name       string      `json:"name"`
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(b, &prof); err != nil {
		t.Fatal(err)
	}
	if prof.Name != "Priya Shah" || !reflect.DeepEqual(prof.Embeddings, [][]float32{{1, 0, 0}, {0, 1, 0}}) {
		t.Fatalf("profile = %+v", prof)
	}
	if name, _, ok := v.Match([]float32{0, 1, 0}); !ok || name != "Priya Shah" {
		t.Fatalf("second embedding should match: %q %v", name, ok)
	}

	if err := v.Enroll("Bob", []float32{0, 0, 1}); err != nil {
		t.Fatal(err)
	}
	names, err := v.List()
	if err != nil || !reflect.DeepEqual(names, []string{"Bob", "Priya Shah"}) {
		t.Fatalf("List = %v, %v", names, err)
	}
	if name, _, ok := v.Match([]float32{0, 0.1, 1}); !ok || name != "Bob" {
		t.Fatalf("best match should be Bob, got %q %v", name, ok)
	}

	if err := v.Delete("Priya Shah"); err != nil {
		t.Fatal(err)
	}
	if names, _ := v.List(); !reflect.DeepEqual(names, []string{"Bob"}) {
		t.Fatalf("after delete List = %v", names)
	}
	if err := v.Delete("Priya Shah"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleting missing voice: %v", err)
	}
	if err := v.Enroll("!!!", []float32{1}); err == nil {
		t.Fatal("want error for name without a usable slug")
	}
	if err := v.Enroll("Empty", nil); err == nil {
		t.Fatal("want error for empty embedding")
	}
}

func TestNoopDiarizer(t *testing.T) {
	var d Diarizer = NoopDiarizer{}
	turns, embs, err := d.Diarize(context.Background(), "x.wav")
	if err != nil || len(turns) != 0 || len(embs) != 0 {
		t.Fatalf("Noop = %v %v %v", turns, embs, err)
	}
}
