//go:build !linux && !darwin

package monitor

import "errors"

func processCPUSeconds() (float64, error) {
	return 0, errors.New("process CPU probe unsupported on this platform")
}
