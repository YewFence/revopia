package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/volume"
)

func helperContainerName(volumeName string) string {
	return helperNamePrefix + hashString(volumeName)
}

func restoreHelperContainerName(sessionID string) string {
	return restoreHelperNamePrefix + hashString(sessionID)
}

func cleanupUnmountContainerName(friendlyName string) string {
	return cleanupUnmountNamePrefix + hashString(friendlyName)
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func defaultRestoreSessionID(sourceVolume, targetVolume string) string {
	stamp := time.Now().UTC().Format("20060102-150405")
	return sanitizePathName(sourceVolume) + "-to-" + sanitizePathName(targetVolume) + "-" + stamp
}

func defaultRestoreTargetVolume(sourceVolume string) string {
	stamp := time.Now().UTC().Format("20060102-150405")
	return strings.TrimSpace(sourceVolume) + "-restore-" + stamp
}

func friendlyNameForVolume(vol volume.Volume) (string, error) {
	if name := sanitizePathName(vol.Labels[labelBackupName]); name != "" {
		return name, nil
	}
	if name := sanitizePathName(vol.Name); name != "" {
		return name, nil
	}
	return "", fmt.Errorf("volume %q 没有可用的路径安全名称，已跳过", vol.Name)
}

func sanitizePathName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		if isSafeNameRune(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	name := strings.Trim(b.String(), ".-")
	if name == "." || name == ".." {
		return ""
	}
	return name
}

func isSafeNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' ||
		r == '.' ||
		r == '_' ||
		r == '-'
}

func containerHasName(summary container.Summary, name string) bool {
	for _, item := range summary.Names {
		if strings.TrimPrefix(item, "/") == name {
			return true
		}
	}
	return false
}
