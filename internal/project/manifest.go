package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nonfiction/nf/internal/config"
)

const ManifestVersion = 2

type Manifest struct {
	Version   int        `json:"version"`
	Project   Project    `json:"project"`
	WordPress WordPress  `json:"wordpress"`
	Local     *Local     `json:"local,omitempty"`
	Remotes   RemoteRefs `json:"remotes,omitempty"`
}

type Project struct {
	Slug            string `json:"slug"`
	PasswordVersion uint64 `json:"password_version"`

	passwordVersionSet bool
}

type WordPress struct {
	Themes  []any             `json:"themes"`
	Plugins []any             `json:"plugins,omitempty"`
	Defines []any             `json:"defines,omitempty"`
	Aliases map[string]string `json:"aliases,omitempty"`
}

type Local struct {
	Compose          string `json:"compose,omitempty"`
	WordPressService string `json:"wordpress_service,omitempty"`
	UploadsPath      string `json:"uploads_path,omitempty"`
	AdminUser        string `json:"admin_user,omitempty"`
	Ports            *Ports `json:"ports,omitempty"`
}

type Ports struct {
	WordPress int `json:"wordpress,omitempty"`
	Mailpit   int `json:"mailpit,omitempty"`
	DB        int `json:"db,omitempty"`
}

type RemoteRefs map[string]string

func (p *Project) UnmarshalJSON(data []byte) error {
	type projectJSON struct {
		Slug            string  `json:"slug"`
		PasswordVersion *uint64 `json:"password_version"`
	}
	var decoded projectJSON
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoded.PasswordVersion == nil {
		return fmt.Errorf("password_version is required")
	}
	p.Slug = decoded.Slug
	p.PasswordVersion = *decoded.PasswordVersion
	p.passwordVersionSet = true
	return nil
}

func Load(root string) (*Manifest, error) {
	path := config.ProjectFile(root)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected data after JSON document")
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return &manifest, nil
}

func (m *Manifest) Validate() error {
	if m == nil {
		return fmt.Errorf("nf.json manifest is required")
	}
	if m.Version != ManifestVersion {
		return fmt.Errorf("nf.json version must be %d", ManifestVersion)
	}
	if err := validateSlug(m.Project.Slug); err != nil {
		return fmt.Errorf("nf.json project.slug: %w", err)
	}
	if len(m.WordPress.Themes) == 0 {
		return fmt.Errorf("nf.json wordpress.themes must include at least one theme")
	}
	if m.Local != nil && m.Local.Ports != nil {
		for name, port := range map[string]int{
			"wordpress": m.Local.Ports.WordPress,
			"mailpit":   m.Local.Ports.Mailpit,
			"db":        m.Local.Ports.DB,
		} {
			if port < 0 || port > 65535 {
				return fmt.Errorf("nf.json local.ports.%s must be between 1 and 65535", name)
			}
		}
	}
	if m.Local != nil {
		if err := ValidateRelativePath(m.Local.UploadsPath); err != nil {
			return fmt.Errorf("nf.json local.uploads_path: %w", err)
		}
		if strings.ContainsAny(m.Local.AdminUser, "\r\n\x00") {
			return fmt.Errorf("nf.json local.admin_user must not contain line breaks")
		}
		if service := strings.TrimSpace(m.Local.WordPressService); service != "" && !isSafeName(service) {
			return fmt.Errorf("nf.json local.wordpress_service must use only letters, numbers, dots, underscores, and hyphens")
		}
	}
	return nil
}

func Save(root string, manifest *Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("marshal nf.json: %w", err)
	}

	projectPath := config.ProjectFile(root)
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(projectPath); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(projectPath), ".nf.json-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(output.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, projectPath); err != nil {
		return err
	}
	return nil
}

func validateSlug(value string) error {
	return ValidateName(value)
}

func ValidateName(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("must not be empty")
	}
	if value == "." || value == ".." || !isSafeName(value) {
		return fmt.Errorf("must be one directory-safe name")
	}
	return nil
}

func ValidateRelativePath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._-/", char) {
			continue
		}
		return fmt.Errorf("must use only letters, numbers, dots, underscores, hyphens, and slashes")
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("must be a relative path within the managed env directory")
	}
	return nil
}

func isSafeName(value string) bool {
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._-", char) {
			continue
		}
		return false
	}
	return value != ""
}
