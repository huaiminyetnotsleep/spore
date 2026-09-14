//go:build linux

package monitor

import "syscall"

// processCPUSeconds 返回进程累计 CPU 时间（user + sys，秒）。
// 用于相邻采样点差分出 CPU 占用率；getrusage 无需 CGO。
func processCPUSeconds() (float64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	return float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6 +
		float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6, nil
}
