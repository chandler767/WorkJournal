package diarize

import (
	"math/rand"
	"reflect"
	"testing"
)

// noisy returns base plus uniform noise of the given amplitude per element.
func noisy(r *rand.Rand, base []float32, amp float32) []float32 {
	out := make([]float32, len(base))
	for i, x := range base {
		out[i] = x + amp*(2*r.Float32()-1)
	}
	return out
}

// voice returns a 16-dim unit-ish vector concentrated on dimension d.
func voice(d int) []float32 {
	v := make([]float32, 16)
	v[d] = 1
	return v
}

func TestClusterThreeVoicesAcrossChunks(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	voices := [][]float32{voice(0), voice(5), voice(10)}
	// Four chunks; each chunk has its own local labels (over-split: one
	// voice appears as two local speakers in chunk 2).
	var items []ClusterItem
	var want []int
	order := [][]int{{0, 1}, {1, 2, 0}, {2, 2, 1}, {0, 1, 2}}
	for _, chunk := range order {
		for _, v := range chunk {
			items = append(items, ClusterItem{Emb: noisy(r, voices[v], 0.35), Seconds: 40})
			want = append(want, v)
		}
	}
	got := Cluster(items, 0.45, 20)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clusters = %v, want %v", got, want)
	}
}

func TestClusterNumbersByFirstItem(t *testing.T) {
	items := []ClusterItem{
		{Emb: []float32{0, 1, 0}, Seconds: 30},
		{Emb: []float32{1, 0, 0}, Seconds: 30},
		{Emb: []float32{0, 0.95, 0.1}, Seconds: 30},
	}
	if got := Cluster(items, 0.45, 20); !reflect.DeepEqual(got, []int{0, 1, 0}) {
		t.Fatalf("clusters = %v", got)
	}
}

func TestClusterFragments(t *testing.T) {
	items := []ClusterItem{
		{Emb: []float32{1, 0, 0, 0, 0}, Seconds: 60},
		{Emb: []float32{0, 1, 0, 0, 0}, Seconds: 60},
		// A short fragment close enough to voice 0 for the relaxed bar
		// (0.45-0.15 = 0.30) but not the clustering one.
		{Emb: []float32{0.4, 0, 0.92, 0, 0}, Seconds: 3},
		// A short fragment like nobody: unknown.
		{Emb: []float32{0, 0, 0, 1, 0}, Seconds: 5},
		// Two fragments of the same voice that together pass the bar.
		{Emb: []float32{0, 0, 0, 0, 1}, Seconds: 12},
		{Emb: []float32{0, 0, 0, 0.05, 1}, Seconds: 12},
	}
	got := Cluster(items, 0.45, 20)
	want := []int{0, 1, 0, -1, 2, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clusters = %v, want %v", got, want)
	}
}

func TestClusterNoRealVoice(t *testing.T) {
	items := []ClusterItem{{Emb: []float32{1, 0}, Seconds: 5}, {Emb: []float32{0, 1}, Seconds: 5}}
	if got := Cluster(items, 0.45, 20); !reflect.DeepEqual(got, []int{-1, -1}) {
		t.Fatalf("clusters = %v", got)
	}
}

func TestClusterWithoutEmbeddingStaysAlone(t *testing.T) {
	items := []ClusterItem{
		{Emb: []float32{1, 0}, Seconds: 30},
		{Seconds: 30},
		{Emb: []float32{1, 0}, Seconds: 30},
		{Seconds: 2},
	}
	if got := Cluster(items, 0.45, 20); !reflect.DeepEqual(got, []int{0, 1, 0, -1}) {
		t.Fatalf("clusters = %v", got)
	}
}

func TestClusterWeightsByDuration(t *testing.T) {
	// A and C are far apart; B sits between them, nearer C. Cluster
	// similarity is the speech-weighted average of the pairwise ones.
	items := []ClusterItem{
		{Emb: []float32{1, 0}, Seconds: 300},
		{Emb: []float32{0.6, 0.8}, Seconds: 300},
		{Emb: []float32{0, 1}, Seconds: 300},
	}
	// cos(A,B)=0.6, cos(B,C)=0.8, cos(A,C)=0: B joins C at 0.8; then
	// sim(A,{B,C}) = (0.6*300 + 0*300)/600 = 0.3 < 0.45.
	if got := Cluster(items, 0.45, 20); !reflect.DeepEqual(got, []int{0, 1, 1}) {
		t.Fatalf("clusters = %v", got)
	}
	// With B dominant in speech, {B,C}'s similarity to A rises to
	// (0.6*3000+0)/3300 = 0.545 >= 0.45: all one voice.
	items[1].Seconds = 3000
	if got := Cluster(items, 0.45, 20); !reflect.DeepEqual(got, []int{0, 0, 0}) {
		t.Fatalf("weighted clusters = %v", got)
	}
}
