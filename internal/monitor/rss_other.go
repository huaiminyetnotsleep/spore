//go:build !linux && !darwin

package monitor

import "errors"

func processRSS() (int64, error) {
	return 0, errors.New("process RSS probe unsupported on this platform")
}
