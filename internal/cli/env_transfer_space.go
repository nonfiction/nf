package cli

// Disk-space and transfer helpers for env push/pull.

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
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

func envRemoteUploadsRsyncArgs(target envRemoteSyncTarget, source, destination string, push bool) []string {
	args := []string{"rsync", "-az", "--delete", "--progress", "--no-owner", "--no-group"}
	if target.SudoFileOps {
		args = append(args, "--rsync-path=sudo rsync")
		if push {
			args = append(args, "--chown=www-data:www-data")
		}
	}
	args = append(args, "-e", "ssh -p "+target.SSHPort, source, destination)
	return args
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
	fmt.Printf("Checking available disk space for local %s...\n", label)
	available, err := localAvailableDiskBytesFn(path)
	if err != nil {
		return fmt.Errorf("check local disk space for %s: %w", label, err)
	}
	return ensureDiskSpace("local "+label, path, requiredBytes, available)
}

func ensureRemoteDiskSpace(target envRemoteSyncTarget, path, label string, requiredBytes int64) error {
	fmt.Printf("Checking available disk space for remote %s...\n", label)
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
		requiredText, availableText := formatEnvDiskSpacePair(requiredBytes, availableBytes)
		return fmt.Errorf("not enough disk space for %s at %s: need %s, available %s", label, path, requiredText, availableText)
	}
	fmt.Printf("Disk space ok for %s: need %s, available %s\n", label, formatEnvSnapshotSize(requiredBytes), formatEnvSnapshotSize(availableBytes))
	return nil
}

func formatEnvDiskSpacePair(requiredBytes, availableBytes int64) (string, string) {
	requiredText := formatEnvSnapshotSize(requiredBytes)
	availableText := formatEnvSnapshotSize(availableBytes)
	if requiredText == availableText && requiredBytes != availableBytes {
		requiredText = fmt.Sprintf("%s (%d bytes)", requiredText, requiredBytes)
		availableText = fmt.Sprintf("%s (%d bytes)", availableText, availableBytes)
	}
	return requiredText, availableText
}

func ensureLocalSnapshotCreateDiskSpace(cfg envConfig) (int64, error) {
	estimate, err := localWordPressTransferEstimateBytesFn(cfg)
	if err != nil {
		return 0, err
	}
	if err := ensureLocalDiskSpace(envSnapshotProjectDir(cfg), "snapshot workspace", envTransferRequiredBytes(estimate)); err != nil {
		return 0, err
	}
	return estimate, nil
}

func ensureLocalPushTransferCreateDiskSpace(cfg envConfig) (int64, error) {
	estimate, err := localPushTransferEstimateBytesFn(cfg)
	if err != nil {
		return 0, err
	}
	if err := ensureLocalDiskSpace(envSnapshotProjectDir(cfg), "push transfer workspace", envTransferRequiredBytes(estimate)); err != nil {
		return 0, err
	}
	return estimate, nil
}

func ensureLocalSnapshotRestoreDiskSpace(cfg envConfig, name string) error {
	expandedSize, err := localSnapshotExpandedSizeBytesFn(cfg, name)
	if err != nil {
		return err
	}
	return ensureLocalDiskSpace(envSnapshotDir(cfg, name), "restore workspace", envTransferRequiredBytes(expandedSize))
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

func localWordPressTransferEstimateBytes(cfg envConfig) (int64, error) {
	return localWordPressTransferEstimateBytesWithUploads(cfg, true)
}

func localPushTransferEstimateBytes(cfg envConfig) (int64, error) {
	return localWordPressTransferEstimateBytesWithUploads(cfg, false)
}

func localWordPressTransferEstimateBytesWithUploads(cfg envConfig, includeUploads bool) (int64, error) {
	output, err := runCommandSpecOutputSilentFn(execSpec{Dir: localEnvDir(cfg), Args: envSnapshotComposeArgs(cfg, localWordPressTransferEstimateScript(includeUploads))})
	if err != nil {
		return 0, err
	}
	return parseDiskBytes([]byte(output))
}

func localSnapshotExpandedSizeBytes(cfg envConfig, name string) (int64, error) {
	databaseSize, err := gzipUncompressedSizeBytes(envSnapshotHostDatabaseArchive(cfg, name))
	if err != nil {
		return 0, err
	}
	wpContentSize, err := tarGzExpandedSizeBytes(envSnapshotHostWpContentArchive(cfg, name))
	if err != nil {
		return 0, err
	}
	return addEnvTransferBytes(databaseSize, wpContentSize), nil
}

func localPushTransferArchiveSizeBytes(cfg envConfig, name string) (int64, error) {
	databaseSize, err := localPathSizeBytes(envSnapshotHostDatabaseArchive(cfg, name))
	if err != nil {
		return 0, err
	}
	wpContentSize, err := localPathSizeBytes(envSnapshotHostWpContentTransferArchive(cfg, name))
	if err != nil {
		return 0, err
	}
	return addEnvTransferBytes(databaseSize, wpContentSize), nil
}

func localPushTransferExpandedSizeBytes(cfg envConfig, name string) (int64, error) {
	databaseSize, err := gzipUncompressedSizeBytes(envSnapshotHostDatabaseArchive(cfg, name))
	if err != nil {
		return 0, err
	}
	wpContentSize, err := tarGzExpandedSizeBytes(envSnapshotHostWpContentTransferArchive(cfg, name))
	if err != nil {
		return 0, err
	}
	return addEnvTransferBytes(databaseSize, wpContentSize), nil
}

func gzipUncompressedSizeBytes(filePath string) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	written, err := io.Copy(io.Discard, reader)
	if err != nil {
		return 0, err
	}
	return written, nil
}

func tarGzExpandedSizeBytes(filePath string) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return 0, err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	total := int64(0)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		if total > maxInt64-header.Size {
			return maxInt64, nil
		}
		total += header.Size
	}
	return total, nil
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
	return remoteWordPressTransferEstimateBytesWithUploads(target, true)
}

func remoteWordPressTransferEstimateBytesWithUploads(target envRemoteSyncTarget, includeUploads bool) (int64, error) {
	output, err := runSSHOutputFn(remoteSSHArgs(target, remoteWordPressTransferEstimateScript(target, includeUploads)))
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

func localWordPressTransferEstimateScript(includeUploads bool) string {
	sql := shellQuoteArg("SELECT COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables WHERE table_schema = DATABASE()")
	dirs := "wp-content/plugins wp-content/mu-plugins wp-content/languages"
	if includeUploads {
		dirs = "wp-content/uploads " + dirs
	}
	return fmt.Sprintf(`set -eu
wp_path=/var/www/html
bytes=0
for dir in %s; do
  if [ -e "$wp_path/$dir" ]; then
    kb=$(du -sk "$wp_path/$dir" 2>/dev/null | awk 'NR==1 { printf "%%.0f\n", $1 }')
    case "$kb" in ''|*[!0-9]*) kb=0 ;; esac
    bytes=$((bytes + kb * 1024))
  fi
done
db_bytes=$(wp --path="$wp_path" db query %s --skip-column-names 2>/dev/null | awk 'NR==1 { gsub(/[^0-9]/, "", $0); print $0 }' || true)
case "$db_bytes" in ''|*[!0-9]*) db_bytes=0 ;; esac
bytes=$((bytes + db_bytes))
printf '%%s\n' "$bytes"
`, dirs, sql)
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

func remoteWordPressTransferEstimateScript(target envRemoteSyncTarget, includeUploads bool) string {
	wpPath := shellQuoteArg(target.WordPressPath)
	sql := shellQuoteArg("SELECT COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables WHERE table_schema = DATABASE()")
	dirs := "wp-content/plugins wp-content/mu-plugins wp-content/languages"
	if includeUploads {
		dirs = "wp-content/uploads " + dirs
	}
	return fmt.Sprintf(`set -eu
wp_path=%s
bytes=0
for dir in %s; do
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
`, wpPath, dirs, target.WPCommand, wpPath, sql)
}

func cleanupRemoteTempDir(target envRemoteSyncTarget, remotePath string) {
	if !isSafeRemoteTempDir(remotePath) {
		fmt.Fprintf(os.Stderr, "Warning: refusing to clean unsafe remote temp dir %q.\n", remotePath)
		return
	}
	if err := runSSHCommandFn(remoteSSHArgs(target, "rm -rf -- "+shellQuoteArg(path.Clean(remotePath)))); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to clean remote temp dir %s: %v\n", remotePath, err)
	}
}

func isSafeRemoteTempDir(remotePath string) bool {
	cleaned := path.Clean(strings.TrimSpace(remotePath))
	if path.Dir(cleaned) != "/tmp" {
		return false
	}
	base := path.Base(cleaned)
	for _, prefix := range []string{"nf-pull-", "nf-push-", "nf-backup-", "nf-snapshot-", "nf-export-"} {
		if strings.HasPrefix(base, prefix) && len(base) > len(prefix) {
			return true
		}
	}
	return false
}
