//go:build linux

package monitor

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func processRSS() (int64, error) {
	f, err := os.Open("/proc/self/statm")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	line := bufio.NewScanner(f)
	if !line.Scan() {
		if err := line.Err(); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("empty /proc/self/statm")
	}
	fields := strings.Fields(line.Text())
	if len(fields) < 2 {
		return 0, fmt.Errorf("invalid /proc/self/statm")
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return pages * int64(os.Getpagesize()), nil
}
