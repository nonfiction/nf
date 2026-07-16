package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validManifest() *Manifest {
	return &Manifest{
		Version: ManifestVersion,
		Project: Project{
			Slug:            "client",
			PasswordVersion: 0,
		},
		WordPress: WordPress{Themes: []any{"twentytwentyfive"}},
	}
}

func TestLoadRejectsInvalidDocuments(t *testing.T) {
	tests := map[string]string{
		"unsupported version": `{"version":1,"project":{"slug":"client","password_version":0},"wordpress":{"themes":["twentytwentyfive"]}}`,
		"unknown field":       `{"version":2,"project":{"slug":"client","password_version":0,"type":"wordpress-theme"},"wordpress":{"themes":["twentytwentyfive"]}}`,
		"missing password":    `{"version":2,"project":{"slug":"client"},"wordpress":{"themes":["twentytwentyfive"]}}`,
		"null password":       `{"version":2,"project":{"slug":"client","password_version":null},"wordpress":{"themes":["twentytwentyfive"]}}`,
		"trailing document":   `{"version":2,"project":{"slug":"client","password_version":0},"wordpress":{"themes":["twentytwentyfive"]}} {}`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "nf.json"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil {
				t.Fatal("Load() error = nil, want rejection")
			}
		})
	}
}

func TestValidateRejectsUnsafeUploadsPath(t *testing.T) {
	for _, uploadsPath := range []string{"../outside", "nested/../../outside", "/tmp/outside", `nested\outside`, ".", "uploads\n- /:/host"} {
		manifest := validManifest()
		manifest.Local = &Local{UploadsPath: uploadsPath}
		if err := manifest.Validate(); err == nil {
			t.Errorf("Validate() with uploads_path %q error = nil, want rejection", uploadsPath)
		}
	}
}

func TestValidateAcceptsNestedUploadsPath(t *testing.T) {
	manifest := validManifest()
	manifest.Local = &Local{UploadsPath: "media/client_uploads"}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestSaveRoundTripsAndPreservesPermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nf.json")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	manifest := validManifest()
	manifest.Local = &Local{UploadsPath: "client-uploads", Ports: &Ports{WordPress: 8080}}
	manifest.WordPress.Themes = []any{map[string]any{
		"slug":   "client",
		"source": "repo",
		"tasks": map[string]any{
			"build": "npm run check && npm run build",
		},
	}}
	if err := Save(root, manifest); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `\u0026`) || !strings.Contains(string(data), `&&`) {
		t.Fatalf("Save() escaped shell operators:\n%s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("nf.json mode = %o, want 640", got)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Local == nil || loaded.Local.UploadsPath != "client-uploads" || loaded.Local.Ports == nil || loaded.Local.Ports.WordPress != 8080 {
		t.Fatalf("Load() local = %#v", loaded.Local)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".nf.json-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestSaveReportsMarshalErrors(t *testing.T) {
	manifest := validManifest()
	manifest.WordPress.Themes = []any{make(chan int)}
	if err := Save(t.TempDir(), manifest); err == nil || !strings.Contains(err.Error(), "marshal nf.json") {
		t.Fatalf("Save() error = %v, want marshal error", err)
	}
}
