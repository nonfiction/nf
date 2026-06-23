package cli

// Disk-space and transfer helpers for env push/pull.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	envTransferDiskBufferBytes int64 = 128 * 1024 * 1024
	maxInt64                         = int64(1<<63 - 1)
)

func envRemoteRsyncArgs(target envRemoteSyncTarget, source, destination string) []string {
	return []string{"rsync", "-az", "--progress", "-e", "ssh -p " + target.SSHPort, source, destination}
}

func envTransferRequiredBytes(size int64) int64 {
	if size < 0 {
		size = 0
	}
	if size > maxInt64-envTransferDiskBufferBytes {
		return maxInt64
	}
	return size + envTransferDiskBufferBytes
}

func addEnvTransferBytes(values ...int64) int64 {
	total := int64(0)
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if total > maxInt64-value {
			return maxInt64
		}
		total += value
	}
	return total
}

func ensureLocalDiskSpace(path, label string, requiredBytes int64) error {
	available, err := localAvailableDiskBytesFn(path)
	if err != nil {
		return fmt.Errorf("check local disk space for %s: %w", label, err)
	}
	return ensureDiskSpace("local "+label, path, requiredBytes, available)
}

func ensureRemoteDiskSpace(target envRemoteSyncTarget, path, label string, requiredBytes int64) error {
	available, err := remoteAvailableDiskBytes(target, path)
	if err != nil {
		return fmt.Errorf("check remote disk space for %s: %w", label, err)
	}
	return ensureDiskSpace("remote "+label, path, requiredBytes, available)
}

func ensureDiskSpace(label, path string, requiredBytes, availableBytes int64) error {
	if requiredBytes <= 0 {
		return nil
	}
	if availableBytes < requiredBytes {
		return fmt.Errorf("not enough disk space for %s at %s: need %s, available %s", label, path, formatEnvSnapshotSize(requiredBytes), formatEnvSnapshotSize(availableBytes))
	}
	fmt.Printf("Disk space ok for %s: need %s, available %s\n", label, formatEnvSnapshotSize(requiredBytes), formatEnvSnapshotSize(availableBytes))
	return nil
}

func localAvailableDiskBytes(path string) (int64, error) {
	existingPath, err := nearestExistingDiskPath(path)
	if err != nil {
		return 0, err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(existingPath, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, nil
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	if available > uint64(maxInt64) {
		return maxInt64, nil
	}
	return int64(available), nil
}

func nearestExistingDiskPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", os.ErrNotExist
		}
		path = parent
	}
}

func localPathSizeBytes(path string) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		if info.Mode().IsRegular() {
			return info.Size(), nil
		}
		return 0, nil
	}
	total := int64(0)
	err = filepath.WalkDir(path, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if total > maxInt64-info.Size() {
			total = maxInt64
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func remoteAvailableDiskBytes(target envRemoteSyncTarget, remotePath string) (int64, error) {
	output, err := runSSHOutputFn(remoteSSHArgs(target, remoteAvailableDiskScript(remotePath)))
	if err != nil {
		return 0, err
	}
	return parseDiskBytes(output)
}

func remotePathSizeBytes(target envRemoteSyncTarget, remotePath string) (int64, error) {
	output, err := runSSHOutputFn(remoteSSHArgs(target, remotePathSizeScript(remotePath)))
	if err != nil {
		return 0, err
	}
	return parseDiskBytes(output)
}

func remoteWordPressTransferEstimateBytes(target envRemoteSyncTarget) (int64, error) {
	output, err := runSSHOutputFn(remoteSSHArgs(target, remoteWordPressTransferEstimateScript(target)))
	if err != nil {
		return 0, err
	}
	return parseDiskBytes(output)
}

func parseDiskBytes(output []byte) (int64, error) {
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty disk-space probe output")
	}
	value, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse disk-space probe output %q: %w", strings.TrimSpace(string(output)), err)
	}
	return value, nil
}

func remoteAvailableDiskScript(remotePath string) string {
	return fmt.Sprintf(`set -eu
df -Pk %s | awk 'NR==2 { printf "%%.0f\n", $4 * 1024 }'
`, shellQuoteArg(remotePath))
}

func remotePathSizeScript(remotePath string) string {
	quoted := shellQuoteArg(remotePath)
	return fmt.Sprintf(`set -eu
if [ -e %s ]; then
  du -sk %s | awk 'NR==1 { printf "%%.0f\n", $1 * 1024 }'
else
  printf '0\n'
fi
`, quoted, quoted)
}

func remoteWordPressTransferEstimateScript(target envRemoteSyncTarget) string {
	wpPath := shellQuoteArg(target.WordPressPath)
	sql := shellQuoteArg("SELECT COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables WHERE table_schema = DATABASE()")
	return fmt.Sprintf(`set -eu
wp_path=%s
bytes=0
for dir in wp-content/uploads wp-content/plugins wp-content/mu-plugins wp-content/languages; do
  if [ -e "$wp_path/$dir" ]; then
    kb=$(du -sk "$wp_path/$dir" 2>/dev/null | awk 'NR==1 { printf "%%.0f\n", $1 }')
    case "$kb" in ''|*[!0-9]*) kb=0 ;; esac
    bytes=$((bytes + kb * 1024))
  fi
done
db_bytes=$(%s --path=%s db query %s --skip-column-names 2>/dev/null | awk 'NR==1 { gsub(/[^0-9]/, "", $0); print $0 }' || true)
case "$db_bytes" in ''|*[!0-9]*) db_bytes=0 ;; esac
bytes=$((bytes + db_bytes))
printf '%%s\n' "$bytes"
`, wpPath, target.WPCommand, wpPath, sql)
}
