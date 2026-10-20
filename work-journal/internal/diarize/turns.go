package diarize

import (
	"cmp"
	"slices"
)

// segment is one sherpa result; speaker is an opaque sherpa cluster id.
type segment struct {
	start, end float64
	speaker    int
}

// planTurns renumbers sherpa's opaque speaker ids to 0..n-1 in order of
// first appearance and picks, per speaker, the sample ranges [from, to) of
// its longest segments, up to capSec seconds in total (the last one
// truncated), clamped to n samples.
func planTurns(segs []segment, n, rate, capSec int) ([]Turn, map[int][][2]int) {
	ids := map[int]int{}
	var turns []Turn
	bySpeaker := map[int][]segment{}
	for _, s := range segs {
		id, ok := ids[s.speaker]
		if !ok {
			id = len(ids)
			ids[s.speaker] = id
		}
		turns = append(turns, Turn{Start: s.start, End: s.end, Speaker: id})
		bySpeaker[id] = append(bySpeaker[id], s)
	}
	limit := capSec * rate
	clips := map[int][][2]int{}
	for id, ss := range bySpeaker {
		slices.SortStableFunc(ss, func(a, b segment) int { return cmp.Compare(b.end-b.start, a.end-a.start) })
		left := limit
		for _, s := range ss {
			from, to := max(int(s.start*float64(rate)), 0), min(int(s.end*float64(rate)), n)
			to = min(to, from+left)
			if from >= to {
				continue
			}
			clips[id] = append(clips[id], [2]int{from, to})
			if left -= to - from; left == 0 {
				break
			}
		}
	}
	return turns, clips
}
