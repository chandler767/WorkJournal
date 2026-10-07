package retention

import "syscall"

// Disk guard states.
const (
	StateNormal     = "normal"
	StateScreensOff = "screens_off"
	StateAudioOff   = "audio_off"
)

// FreeGB returns the free space, in GiB, available to unprivileged users on
// the filesystem holding path.
func FreeGB(path string) (float64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, err
	}
	return float64(fs.Bavail) * float64(fs.Bsize) / (1 << 30), nil
}

// Guard maps free space to a capture state: normal at or above min,
// screens_off at or above min/2, otherwise audio_off.
func Guard(free, min float64) string {
	switch {
	case free >= min:
		return StateNormal
	case free >= min/2:
		return StateScreensOff
	default:
		return StateAudioOff
	}
}

// DiskAction computes the next guard state from the previous one and reports
// whether it changed, i.e. whether the recorder must be sent the new state.
func DiskAction(prev string, free, min float64) (next string, send bool) {
	next = Guard(free, min)
	return next, next != prev
}
