package diarize

import "math"

// ClusterItem is one speaker as a single Diarize call saw it: its
// embedding and how many seconds of speech it has.
type ClusterItem struct {
	Emb     []float32
	Seconds float64
}

// FragmentMargin is how much lower than the clustering threshold a short
// cluster's similarity may be when it joins its nearest real cluster.
const FragmentMargin = 0.15

// minWeight keeps an item with no counted speech from vanishing from its
// cluster's average.
const minWeight = 0.1

// Cluster groups items that sound like the same voice, by agglomerative
// clustering on cosine similarity with speech-weighted average linkage: the
// similarity of two clusters is the average of their items' pairwise
// similarities, each pair weighted by both items' seconds. The two most
// similar clusters merge while their similarity is at least threshold.
//
// A resulting cluster with less than minSeconds of speech is a fragment: it
// joins the most similar real cluster (one with at least minSeconds) when
// their similarity is at least threshold-FragmentMargin, else its items get
// -1 (unknown). An item without an embedding stays in a cluster of its own.
//
// It returns each item's cluster, numbered 0, 1, ... in order of each
// cluster's first item, so callers pass items in order of appearance.
func Cluster(items []ClusterItem, threshold, minSeconds float64) []int {
	n := len(items)
	dim := 0
	for _, it := range items {
		if len(it.Emb) > 0 {
			dim = len(it.Emb)
			break
		}
	}
	// Each cluster keeps sum = Σ w·unit(emb) and weight = Σ w, so the
	// weighted average of pairwise cosines is dot(sumA, sumB)/(wA·wB).
	type cluster struct {
		sum     []float64 // nil: no usable embedding
		weight  float64
		seconds float64
		members []int
		alive   bool
		best    int
		bestSim float64
	}
	cs := make([]*cluster, n)
	for i, it := range items {
		w := math.Max(it.Seconds, minWeight)
		c := &cluster{weight: w, seconds: it.Seconds, members: []int{i}, alive: true, best: -1}
		if u := unitVec(it.Emb); u != nil && len(it.Emb) == dim {
			c.sum = make([]float64, dim)
			for k, x := range u {
				c.sum[k] = w * x
			}
		}
		cs[i] = c
	}
	sim := func(a, b *cluster) float64 {
		if a.sum == nil || b.sum == nil {
			return math.Inf(-1)
		}
		var dot float64
		for k := range a.sum {
			dot += a.sum[k] * b.sum[k]
		}
		return dot / (a.weight * b.weight)
	}
	refresh := func(i int) {
		c := cs[i]
		c.best, c.bestSim = -1, math.Inf(-1)
		for j, o := range cs {
			if j == i || !o.alive {
				continue
			}
			if s := sim(c, o); s > c.bestSim {
				c.best, c.bestSim = j, s
			}
		}
	}
	for i := range cs {
		refresh(i)
	}
	for {
		a := -1
		for i, c := range cs {
			if c.alive && c.best >= 0 && (a < 0 || c.bestSim > cs[a].bestSim) {
				a = i
			}
		}
		if a < 0 || cs[a].bestSim < threshold {
			break
		}
		b := cs[a].best
		if b < a {
			a, b = b, a
		}
		ca, cb := cs[a], cs[b]
		for k := range ca.sum {
			ca.sum[k] += cb.sum[k]
		}
		ca.weight += cb.weight
		ca.seconds += cb.seconds
		ca.members = append(ca.members, cb.members...)
		cb.alive = false
		refresh(a)
		for i, c := range cs {
			if i == a || !c.alive {
				continue
			}
			if c.best == a || c.best == b {
				refresh(i)
			} else if s := sim(c, ca); s > c.bestSim {
				c.best, c.bestSim = a, s
			}
		}
	}

	owner := make([]int, n) // item -> surviving cluster index, or -1
	var real []int
	for i, c := range cs {
		if !c.alive {
			continue
		}
		for _, m := range c.members {
			owner[m] = i
		}
		if c.seconds >= minSeconds {
			real = append(real, i)
		}
	}
	for _, c := range cs {
		if !c.alive || c.seconds >= minSeconds {
			continue
		}
		to, best := -1, threshold-FragmentMargin
		for _, r := range real {
			if s := sim(c, cs[r]); s >= best {
				to, best = r, s
			}
		}
		for _, m := range c.members {
			owner[m] = to
		}
	}

	out := make([]int, n)
	ids := map[int]int{}
	for i := range items {
		o := owner[i]
		if o < 0 {
			out[i] = -1
			continue
		}
		id, ok := ids[o]
		if !ok {
			id = len(ids)
			ids[o] = id
		}
		out[i] = id
	}
	return out
}

// unitVec returns v scaled to length 1, or nil for an empty or zero vector.
func unitVec(v []float32) []float64 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return nil
	}
	n = math.Sqrt(n)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x) / n
	}
	return out
}
