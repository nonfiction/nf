package passwords

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/awnumar/agekd"
)

const ageIdentityContext = "github.com/nonfiction/nf/age-identity/v1"

func DeriveAgeIdentity(salt string) (identity, recipient string, err error) {
	if salt == "" {
		return "", "", PasswordError{Msg: "cannot derive an age identity from an empty password salt"}
	}
	derived, err := agekd.X25519IdentityFromKey([]byte(salt), []byte(ageIdentityContext))
	if err != nil {
		return "", "", fmt.Errorf("derive age identity: %w", err)
	}
	return derived.String(), derived.Recipient().String(), nil
}

func EnsureAgeIdentity(path, salt string) (string, error) {
	identity, recipient, err := DeriveAgeIdentity(salt)
	if err != nil {
		return "", err
	}
	desired := []byte(identity + "\n")
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create age identity directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure age identity directory: %w", err)
	}

	info, statErr := os.Lstat(path)
	if statErr == nil && info.Mode().IsRegular() {
		current, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read age identity: %w", err)
		}
		if bytes.Equal(current, desired) {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", fmt.Errorf("secure age identity: %w", err)
			}
			return recipient, nil
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect age identity: %w", statErr)
	}

	temp, err := os.CreateTemp(dir, ".age-identity-*")
	if err != nil {
		return "", fmt.Errorf("create temporary age identity: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("secure temporary age identity: %w", err)
	}
	if _, err := temp.Write(desired); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("write temporary age identity: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("sync temporary age identity: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close temporary age identity: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("replace age identity: %w", err)
	}
	return recipient, nil
}
