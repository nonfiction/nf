package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nonfiction/nf/internal/project"
)

func TestValidateProjectMetadataRejectsUnknownLeafFields(t *testing.T) {
	tests := map[string]func(*projectMetadata){
		"theme": func(metadata *projectMetadata) {
			metadata.WordPress.Themes = []any{map[string]any{"slug": "client", "source": "repo", "unknown": true}}
		},
		"plugin": func(metadata *projectMetadata) {
			metadata.WordPress.Plugins = []any{map[string]any{"slug": "akismet", "unknown": true}}
		},
		"define": func(metadata *projectMetadata) {
			metadata.WordPress.Defines = []any{map[string]any{"name": "CLIENT_KEY", "value": "value", "unknown": true}}
		},
		"define value": func(metadata *projectMetadata) {
			metadata.WordPress.Defines = []any{map[string]any{
				"name":   "CLIENT_KEY",
				"values": map[string]any{"live": map[string]any{"env": "CLIENT_KEY", "unknown": true}},
			}}
		},
		"package": func(metadata *projectMetadata) {
			metadata.WordPress.Themes = []any{map[string]any{
				"slug": "client", "source": "repo", "package": map[string]any{"output": "dist/client.zip", "unknown": true},
			}}
		},
		"task": func(metadata *projectMetadata) {
			metadata.WordPress.Themes = []any{map[string]any{
				"slug": "client", "source": "repo", "tasks": map[string]any{
					"build": map[string]any{"description": "Build", "run": "npm run build", "unknown": true},
				},
			}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			metadata := &projectMetadata{
				Version:   project.ManifestVersion,
				Project:   project.Project{Slug: "client"},
				WordPress: project.WordPress{Themes: []any{"twentytwentyfive"}},
			}
			mutate(metadata)
			err := validateProjectMetadata(metadata)
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("validateProjectMetadata() error = %v, want unknown field rejection", err)
			}
		})
	}
}

func TestValidateProjectMetadataRejectsUnsafePluginSlug(t *testing.T) {
	metadata := &projectMetadata{
		Version: project.ManifestVersion,
		Project: project.Project{Slug: "client"},
		WordPress: project.WordPress{
			Themes:  []any{"twentytwentyfive"},
			Plugins: []any{map[string]any{"slug": "../../outside", "source": "cache"}},
		},
	}
	if err := validateProjectMetadata(metadata); err == nil {
		t.Fatal("validateProjectMetadata() error = nil, want unsafe plugin slug rejection")
	}
}

func TestPluginAddRejectsUnsafeSlug(t *testing.T) {
	root := t.TempDir()
	metadata := &projectMetadata{
		Version:   project.ManifestVersion,
		Project:   project.Project{Slug: "client"},
		WordPress: project.WordPress{Themes: []any{"twentytwentyfive"}},
	}
	if got := cmdEnvPluginsAdd(root, metadata, envPluginAddOptions{Slug: "../../outside"}); got != 1 {
		t.Fatalf("cmdEnvPluginsAdd() = %d, want 1", got)
	}
	if len(metadata.WordPress.Plugins) != 0 {
		t.Fatalf("plugins = %#v, want unchanged", metadata.WordPress.Plugins)
	}
}

func TestValidateProjectMetadataAllowsOptionalRecipeFields(t *testing.T) {
	metadata := &projectMetadata{
		Version: project.ManifestVersion,
		Project: project.Project{Slug: "client"},
		WordPress: project.WordPress{Themes: []any{map[string]any{
			"slug": "client", "source": "repo", "package": map[string]any{},
			"tasks": map[string]any{"test": map[string]any{"run": "npm test"}},
		}}},
	}
	if err := validateProjectMetadata(metadata); err != nil {
		t.Fatalf("validateProjectMetadata() error = %v", err)
	}
	tasks, err := loadProjectTasksFromMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if got := tasks["test"].Description; got != "npm test" {
		t.Fatalf("task description = %q, want command fallback", got)
	}
}

func TestValidateProjectMetadataRejectsRecipesOnNonRepoTheme(t *testing.T) {
	metadata := &projectMetadata{
		Version: project.ManifestVersion,
		Project: project.Project{Slug: "client"},
		WordPress: project.WordPress{Themes: []any{map[string]any{
			"slug": "twentytwentyfive", "package": map[string]any{},
		}}},
	}
	if err := validateProjectMetadata(metadata); err == nil || !strings.Contains(err.Error(), "only supported for repo themes") {
		t.Fatalf("validateProjectMetadata() error = %v, want recipe ownership rejection", err)
	}
}

func TestValidateProjectMetadataRejectsUnsafeRepoThemePath(t *testing.T) {
	for _, themePath := range []string{"../outside", "/tmp/outside", "theme\n- /:/host"} {
		metadata := &projectMetadata{
			Version: project.ManifestVersion,
			Project: project.Project{Slug: "client"},
			WordPress: project.WordPress{Themes: []any{map[string]any{
				"slug": "client", "source": "repo", "path": themePath,
			}}},
		}
		if err := validateProjectMetadata(metadata); err == nil {
			t.Errorf("validateProjectMetadata() with theme path %q error = nil, want rejection", themePath)
		}
	}
}

func TestValidateProjectMetadataRejectsNonStringLeafFields(t *testing.T) {
	tests := map[string]func(*projectMetadata){
		"theme slug": func(metadata *projectMetadata) {
			metadata.WordPress.Themes = []any{map[string]any{"slug": 123}}
		},
		"theme path": func(metadata *projectMetadata) {
			metadata.WordPress.Themes = []any{map[string]any{"slug": "client", "source": "repo", "path": true}}
		},
		"plugin source": func(metadata *projectMetadata) {
			metadata.WordPress.Plugins = []any{map[string]any{"slug": "akismet", "source": 123}}
		},
		"plugin note": func(metadata *projectMetadata) {
			metadata.WordPress.Plugins = []any{map[string]any{"slug": "akismet", "note": false}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			metadata := &projectMetadata{
				Version:   project.ManifestVersion,
				Project:   project.Project{Slug: "client"},
				WordPress: project.WordPress{Themes: []any{"twentytwentyfive"}},
			}
			mutate(metadata)
			if err := validateProjectMetadata(metadata); err == nil || !strings.Contains(err.Error(), "must be a string") {
				t.Fatalf("validateProjectMetadata() error = %v, want string type rejection", err)
			}
		})
	}
}

func TestValidateProjectMetadataRejectsInvalidDefines(t *testing.T) {
	tests := []map[string]any{
		{"name": "invalid-name", "value": "value"},
		{"name": "CLIENT_KEY", "value": "value", "values": map[string]any{"live": map[string]any{"value": "other"}}},
		{"name": "CLIENT_KEY", "values": map[string]any{}},
	}
	for _, define := range tests {
		metadata := &projectMetadata{
			Version: project.ManifestVersion,
			Project: project.Project{Slug: "client"},
			WordPress: project.WordPress{
				Themes:  []any{"twentytwentyfive"},
				Defines: []any{define},
			},
		}
		if err := validateProjectMetadata(metadata); err == nil {
			t.Errorf("validateProjectMetadata() with define %#v error = nil, want rejection", define)
		}
	}
}

func TestResolveSiteTargetWithoutProjectManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	resolved, metadata, projectFileExists, aliasUsed, err := resolveSiteTarget("client.linode1:live")
	if err != nil {
		t.Fatalf("resolveSiteTarget() error = %v", err)
	}
	if resolved != "client.linode1:live" || metadata != nil || projectFileExists || aliasUsed {
		t.Fatalf("resolveSiteTarget() = %q, %#v, %t, %t", resolved, metadata, projectFileExists, aliasUsed)
	}
}

func TestProjectInitMetadataKeepsTaskCommandsReadable(t *testing.T) {
	root := t.TempDir()
	metadata := projectInitMetadata(projectInitArgs{projectSlug: "client", themeSource: "src/theme"})
	if err := project.Save(root, metadata); err != nil {
		t.Fatalf("project.Save() error = %v", err)
	}
	data, err := os.ReadFile(root + "/nf.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `\u0026`) || !strings.Contains(string(data), `&&`) {
		t.Fatalf("generated tasks escaped shell operators:\n%s", data)
	}
}

func TestWriteProjectInitRejectsUnsafeThemeMetadata(t *testing.T) {
	for name, args := range map[string]projectInitArgs{
		"slug": {projectSlug: "client", themeSlug: "../outside"},
		"path": {projectSlug: "client", themeSource: "../outside"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := writeProjectInit(root, args); err == nil {
				t.Fatal("writeProjectInit() error = nil, want rejection")
			}
			if _, err := os.Stat(filepath.Join(root, "nf.json")); !os.IsNotExist(err) {
				t.Fatalf("nf.json stat error = %v, want file absent", err)
			}
		})
	}
}

func TestValidateProjectMetadataRejectsEmptyTaskCommands(t *testing.T) {
	for _, task := range []any{"", []any{}, map[string]any{"run": ""}, map[string]any{"run": []any{}}} {
		metadata := &projectMetadata{
			Version: project.ManifestVersion,
			Project: project.Project{Slug: "client"},
			WordPress: project.WordPress{Themes: []any{map[string]any{
				"slug": "client", "source": "repo", "tasks": map[string]any{"test": task},
			}}},
		}
		if err := validateProjectMetadata(metadata); err == nil {
			t.Errorf("validateProjectMetadata() with task %#v error = nil, want rejection", task)
		}
	}
}
