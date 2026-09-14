//go:build darwin

package monitor

import (
	"syscall"
)

func processRSS() (int64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	// Darwin reports ru_maxrss in bytes. This is the closest stdlib-only
	// process resident-memory probe available without CGO.
	return usage.Maxrss, nil
}
