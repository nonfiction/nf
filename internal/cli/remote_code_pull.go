package cli

// Shared discovery, classification, and transfer support for plugin/theme pulls.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type wordpressCodeKind string

const (
	wordpressCodePlugin wordpressCodeKind = "plugin"
	wordpressCodeTheme  wordpressCodeKind = "theme"
)

type remoteWordPressCode struct {
	Slug   string
	Active bool
}

func wordpressOrgCodeAvailable(kind wordpressCodeKind, slug string) (bool, error) {
	target := "plugins"
	action := "plugin_information"
	if kind == wordpressCodeTheme {
		target = "themes"
		action = "theme_information"
	}
	query := url.Values{"action": {action}, "request[slug]": {slug}}
	endpoint := "https://api.wordpress.org/" + target + "/info/1.2/?" + query.Encode()
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return false, fmt.Errorf("check WordPress.org for %s %q: %w", kind, slug, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("check WordPress.org for %s %q: unexpected HTTP status %s", kind, slug, response.Status)
	}
	var result struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return false, fmt.Errorf("check WordPress.org for %s %q: invalid response: %w", kind, slug, err)
	}
	if result.Slug != slug {
		return false, fmt.Errorf("check WordPress.org for %s %q: response identified %q", kind, slug, result.Slug)
	}
	return true, nil
}

func remoteWordPressCodeInventory(target envRemoteSyncTarget, kind wordpressCodeKind) ([]remoteWordPressCode, error) {
	script := remoteWordPressCodeInventoryScript(target, kind)
	output, err := runSSHOutputFn(remoteSSHArgs(target, script))
	if err != nil {
		return nil, err
	}
	return parseRemoteWordPressCodeInventory(string(output)), nil
}

func remoteWordPressCodeInventoryScript(target envRemoteSyncTarget, kind wordpressCodeKind) string {
	return fmt.Sprintf(`set -eu
wp_cmd() { %s --path=%s "$@"; }
wp_cmd %s list --fields=name,status --format=csv
`, target.WPCommand, shellQuoteArg(target.WordPressPath), kind)
}

func parseRemoteWordPressCodeInventory(output string) []remoteWordPressCode {
	items := []remoteWordPressCode{}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 2 || fields[0] == "name" {
			continue
		}
		slug := strings.TrimSpace(fields[0])
		if slug == "" || validatePluginSlug(slug) != nil {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		items = append(items, remoteWordPressCode{Slug: slug, Active: strings.TrimSpace(fields[1]) == "active"})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Slug < items[j].Slug })
	return items
}

func remoteActiveTheme(items []remoteWordPressCode) (string, error) {
	for _, item := range items {
		if item.Active {
			return item.Slug, nil
		}
	}
	return "", ProjectError{Msg: "Remote WordPress site did not report an active theme."}
}

func remoteWordPressCodeArchiveScript(target envRemoteSyncTarget, kind wordpressCodeKind, slug, remoteDir string) string {
	collection := string(kind) + "s"
	source := path.Join(target.WordPressPath, "wp-content", collection, slug)
	fileOp := remoteFileOpPrefix(target)
	return fmt.Sprintf(`set -eu
source=%s
archive=%s
if [ ! -d "$source" ]; then printf '%s directory does not exist: %%s\n' "$source" >&2; exit 1; fi
if find "$source" -type l -print -quit | grep -q .; then printf '%s directory contains unsupported symlinks: %%s\n' "$source" >&2; exit 1; fi
rm -rf %s
mkdir -p %s
chmod 777 %s
%star -C %s -czf "$archive" %s
%schmod 644 "$archive"
`, shellQuoteArg(source), shellQuoteArg(path.Join(remoteDir, slug+".tar.gz")), kind, kind, shellQuoteArg(remoteDir), shellQuoteArg(remoteDir), shellQuoteArg(remoteDir), fileOp, shellQuoteArg(path.Dir(source)), shellQuoteArg(slug), fileOp)
}

func downloadRemoteWordPressCode(target envRemoteSyncTarget, kind wordpressCodeKind, slug string) (string, func(), error) {
	if err := validatePluginSlug(slug); err != nil {
		return "", nil, err
	}
	localDir, err := os.MkdirTemp("", "nf-"+string(kind)+"-pull-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(localDir) }
	remoteDir := path.Join("/tmp", "nf-"+string(kind)+"-pull-"+cleanEnvSlug(target.SiteID+"-"+target.Env)+"-"+fmt.Sprint(time.Now().UnixNano()))
	remoteCleanup := func() { _ = runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteDir))) }
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteWordPressCodeArchiveScript(target, kind, slug, remoteDir))); err != nil {
		remoteCleanup()
		cleanup()
		return "", nil, err
	}
	archive := filepath.Join(localDir, slug+".tar.gz")
	args := []string{"rsync", "-az", "-e", remoteRsyncSSH(target), remoteRsyncSource(target, path.Join(remoteDir, slug+".tar.gz")), archive}
	if err := runRsyncCommandFn(args); err != nil {
		remoteCleanup()
		cleanup()
		return "", nil, err
	}
	remoteCleanup()
	if err := extractPulledCodeArchive(archive, localDir, slug); err != nil {
		cleanup()
		return "", nil, err
	}
	sourceDir := filepath.Join(localDir, slug)
	if info, err := os.Stat(sourceDir); err != nil || !info.IsDir() {
		cleanup()
		return "", nil, ProjectError{Msg: fmt.Sprintf("downloaded %s archive does not contain directory %q", kind, slug)}
	}
	return sourceDir, cleanup, nil
}

func extractPulledCodeArchive(sourcePath, destinationDir, root string) error {
	input, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer input.Close()
	gz, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || (name != root && !strings.HasPrefix(name, root+string(filepath.Separator))) {
			return ProjectError{Msg: fmt.Sprintf("downloaded archive contains unsafe path: %s", header.Name)}
		}
		target := filepath.Join(destinationDir, name)
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !header.FileInfo().Mode().IsRegular() {
			return ProjectError{Msg: fmt.Sprintf("downloaded archive contains unsupported entry: %s", header.Name)}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, reader)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}
