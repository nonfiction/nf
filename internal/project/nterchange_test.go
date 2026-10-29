package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNterchangeManifestRoundTrip(t *testing.T) {
	root := t.TempDir()
	m := &Manifest{Version: 2, Project: Project{Slug: "client"}, Nterchange: &Nterchange{Media: []string{"public_html/upload", "public_html/videos"}}, Remotes: RemoteRefs{"review": "client.host:review"}}
	if err := Save(root, m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "nf.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "wordpress") {
		t.Fatalf("nterchange serialized fake WordPress config: %s", data)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Nterchange == nil || len(loaded.Nterchange.MediaPaths()) != 2 {
		t.Fatalf("lost nterchange media: %#v", loaded)
	}
}

func TestNterchangeManifestRejectsMixedOrUnsafeConfiguration(t *testing.T) {
	for _, media := range []string{"../outside", "public_html/../secrets", "public_html", "public_html/.env", "/public_html/upload", "var", "public_html/upload/"} {
		m := &Manifest{Version: 2, Project: Project{Slug: "client"}, Nterchange: &Nterchange{Media: []string{media}}}
		if err := m.Validate(); err == nil {
			t.Errorf("accepted media %q", media)
		}
	}
	for _, media := range [][]string{{"public_html/upload", "public_html/upload"}, {"public_html/upload", "public_html/upload/nested"}} {
		if err := (&Nterchange{Media: media}).Validate(); err == nil {
			t.Errorf("accepted overlapping media %v", media)
		}
	}
	m := validManifest()
	m.Nterchange = &Nterchange{}
	if err := m.Validate(); err == nil {
		t.Fatal("accepted two application kinds")
	}
	m.WordPress = WordPress{}
	m.Local = &Local{}
	if err := m.Validate(); err == nil {
		t.Fatal("accepted WordPress local overrides")
	}
}

func TestNterchangeManifestDefaultsAndUnknownFields(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "nf.json")
	for _, extra := range []string{`"media":[]`, `"media":[],"command":"shell"`} {
		data := `{"version":2,"project":{"slug":"client","password_version":0},"nterchange":{` + extra + `}}`
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		m, err := Load(root)
		if strings.Contains(extra, "command") {
			if err == nil {
				t.Fatal("accepted unknown field")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Nterchange.MediaPaths(); len(got) != 1 || got[0] != "public_html/upload" {
			t.Fatalf("bad default: %v", got)
		}
	}
}
