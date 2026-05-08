package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type visiblePathStatus struct {
	Path        string
	Exists      bool
	IsDir       bool
	IsMount     bool
	EntryCount  int
	EntrySample []string
	Err         string
}

func (s visiblePathStatus) String() string {
	parts := []string{
		"path=" + strconv.Quote(s.Path),
		fmt.Sprintf("exists=%t", s.Exists),
		fmt.Sprintf("dir=%t", s.IsDir),
		fmt.Sprintf("mount=%t", s.IsMount),
		fmt.Sprintf("entries=%d", s.EntryCount),
	}
	if len(s.EntrySample) > 0 {
		parts = append(parts, "sample="+strconv.Quote(strings.Join(s.EntrySample, ",")))
	}
	if s.Err != "" {
		parts = append(parts, "err="+strconv.Quote(s.Err))
	}
	return strings.Join(parts, " ")
}

func waitForVisibleMount(ctx context.Context, cfg Config, spec volumeSpec, logger Logger) (visiblePathStatus, error) {
	deadline := time.Now().Add(cfg.VerifyTimeout)
	var status visiblePathStatus
	for {
		status = inspectVisiblePath(visiblePath(cfg, spec))
		if status.Exists && status.IsMount {
			return status, nil
		}
		if cfg.VerifyTimeout <= 0 || time.Now().After(deadline) {
			if !status.Exists {
				return status, fmt.Errorf("路径不存在")
			}
			if !status.IsMount {
				return status, fmt.Errorf("路径存在但不是挂载点")
			}
			return status, fmt.Errorf("路径不可用")
		}
		logger.Printf("visible_wait volume=%q friendly=%q status=%s", spec.VolumeName, spec.FriendlyName, status.String())
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status, ctx.Err()
		case <-timer.C:
		}
	}
}

func inspectVisiblePath(target string) visiblePathStatus {
	status := visiblePathStatus{Path: filepath.Clean(target)}
	info, err := os.Stat(status.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return status
		}
		status.Err = err.Error()
		return status
	}
	status.Exists = true
	status.IsDir = info.IsDir()

	isMount, err := isMountPoint(status.Path)
	if err != nil {
		status.Err = err.Error()
	} else {
		status.IsMount = isMount
	}

	if status.IsDir {
		entries, err := os.ReadDir(status.Path)
		if err != nil {
			if status.Err == "" {
				status.Err = err.Error()
			}
			return status
		}
		status.EntryCount = len(entries)
		limit := min(len(entries), 8)
		status.EntrySample = make([]string, 0, limit)
		for _, entry := range entries[:limit] {
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			status.EntrySample = append(status.EntrySample, name)
		}
	}
	return status
}

func isMountPoint(target string) (bool, error) {
	_, mounted, err := mountInfoForPath(target)
	return mounted, err
}

func mountInfoForPath(target string) (string, bool, error) {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", false, err
	}
	cleanTarget := filepath.Clean(target)
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mountPoint, err := decodeMountInfoPath(fields[4])
		if err != nil {
			return "", false, err
		}
		if filepath.Clean(mountPoint) == cleanTarget {
			return line, true, nil
		}
	}
	return "", false, nil
}

func decodeMountInfoPath(raw string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			b.WriteByte(raw[i])
			continue
		}
		if i+3 >= len(raw) {
			return "", fmt.Errorf("invalid mountinfo escape %q", raw[i:])
		}
		value, err := strconv.ParseInt(raw[i+1:i+4], 8, 32)
		if err != nil {
			return "", err
		}
		b.WriteByte(byte(value))
		i += 3
	}
	return b.String(), nil
}
