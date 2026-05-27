package passwords

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/config"
)

type PasswordError struct{ Msg string }

func (e PasswordError) Error() string { return e.Msg }

func parseEnvFile(path string) (map[string]string, error) {
	values := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return values, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			if first == last && (first == '"' || first == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func SecretSalt() (string, error) {
	if salt := os.Getenv("NF_SECRET_SALT"); salt != "" {
		return salt, nil
	}
	values, err := parseEnvFile(config.EnvFile())
	if err != nil {
		return "", err
	}
	if salt := values["NF_SECRET_SALT"]; salt != "" {
		return salt, nil
	}
	return "", PasswordError{Msg: "NF_SECRET_SALT is not set in the environment or ~/.config/nf/.env"}
}

func DerivePassword(slug, purpose, salt string) string {
	payload := []byte(fmt.Sprintf("%s:%s:%s", slug, purpose, salt))
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum)[:24]
}
