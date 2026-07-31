package cli

import "testing"

func TestConfigurePulledPluginSources(t *testing.T) {
	metadata := &projectMetadata{}
	metadata.WordPress.Plugins = []any{orderedObject{Pairs: []orderedPair{{Key: "slug", Value: "private-pro"}, {Key: "source", Value: "manual.zip"}, {Key: "activate", Value: false}, {Key: "note", Value: "licensed"}}}}
	if err := configurePulledPlugin(metadata, "private-pro", wordpressPluginCacheSource); err != nil {
		t.Fatal(err)
	}
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 || plugins[0].Source != "cache" || plugins[0].Activate || plugins[0].Note != "licensed" {
		t.Fatalf("configured plugins = %#v", plugins)
	}
	if err := configurePulledPlugin(metadata, "akismet", "wordpress.org"); err != nil {
		t.Fatal(err)
	}
	if got, ok := metadata.WordPress.Plugins[1].(string); !ok || got != "akismet" {
		t.Fatalf("public plugin entry = %#v", metadata.WordPress.Plugins[1])
	}
}

func TestConfigurePulledThemeSources(t *testing.T) {
	metadata := &projectMetadata{}
	metadata.WordPress.Themes = []any{"twentytwentyfive"}
	if err := configurePulledTheme(metadata, "private-theme", wordpressThemeCacheSource); err != nil {
		t.Fatal(err)
	}
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) != 2 || themes[1].Source != "cache" {
		t.Fatalf("configured themes = %#v", themes)
	}
}

func TestParseCachePullArgs(t *testing.T) {
	plugin, ok := parseEnvPluginCacheArgs([]string{"pull", "private-pro", "production"})
	if !ok || plugin.Slug != "private-pro" || plugin.RemoteName != "production" {
		t.Fatalf("plugin cache pull = %#v, %v", plugin, ok)
	}
	theme, ok := parseEnvThemeCacheArgs([]string{"pull"})
	if !ok || theme.Slug != "" || theme.RemoteName != "" {
		t.Fatalf("theme cache pull picker args = %#v, %v", theme, ok)
	}
	if _, ok := parseEnvPluginCacheArgs([]string{"pull", "--bad"}); ok {
		t.Fatal("plugin cache pull accepted a flag as a slug")
	}
	if _, ok := parseEnvThemeCacheArgs([]string{"pull", "private-theme", "--bad"}); ok {
		t.Fatal("theme cache pull accepted a flag as a remote")
	}
}
