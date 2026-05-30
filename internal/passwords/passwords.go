package passwords

import (
	"crypto/sha256"
	"fmt"
	"os"

	"github.com/nonfiction/nf/internal/config"
)

type PasswordError struct{ Msg string }

func (e PasswordError) Error() string { return e.Msg }

func SecretSalt() (string, error) {
	if salt := os.Getenv("NF_SECRET_SALT"); salt != "" {
		return salt, nil
	}
	values, err := config.ReadEnvFile(config.EnvFile())
	if err != nil {
		return "", err
	}
	if salt := values["NF_SECRET_SALT"]; salt != "" {
		return salt, nil
	}
	return "", PasswordError{Msg: "NF_SECRET_SALT is not set in the environment or ~/.config/nf/.env. Run `nf config init` to populate it."}
}

func DerivePassword(slug, purpose, salt string) string {
	payload := []byte(fmt.Sprintf("%s:%s:%s", slug, purpose, salt))
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum)[:24]
}
