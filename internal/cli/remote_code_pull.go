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
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/ui"
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
	return wordpressOrgCodeAvailableWithClient(&http.Client{Timeout: 10 * time.Second}, "https://api.wordpress.org", kind, slug)
}

func wordpressOrgCodeAvailableWithClient(client *http.Client, baseURL string, kind wordpressCodeKind, slug string) (bool, error) {
	target := "plugins"
	action := "plugin_information"
	if kind == wordpressCodeTheme {
		target = "themes"
		action = "theme_information"
	}
	query := url.Values{"action": {action}, "request[slug]": {slug}}
	endpoint := strings.TrimRight(baseURL, "/") + "/" + target + "/info/1.2/?" + query.Encode()
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

func resolveRemoteCodeSelection(metadata *projectMetadata, kind wordpressCodeKind, slug, remoteName, action string) (envRemoteSyncTarget, []remoteWordPressCode, string, error) {
	var err error
	if strings.TrimSpace(remoteName) == "" {
		remoteName, err = chooseProjectRemote(action + " " + string(kind) + " from")
		if err != nil {
			return envRemoteSyncTarget{}, nil, "", err
		}
	}
	target, err := resolveEnvRemoteSyncTarget(string(kind)+" "+action, remoteName, metadata)
	if err != nil {
		return envRemoteSyncTarget{}, nil, "", err
	}
	inventory, err := remoteWordPressCodeInventory(target, kind)
	if err != nil {
		return envRemoteSyncTarget{}, nil, "", err
	}
	if strings.TrimSpace(slug) == "" {
		options := make([]ui.SelectOption, 0, len(inventory))
		for _, item := range inventory {
			label := item.Slug
			if item.Active {
				label += " (active)"
			}
			options = append(options, ui.SelectOption{Value: item.Slug, Label: label})
		}
		if len(options) == 0 {
			return envRemoteSyncTarget{}, nil, "", ProjectError{Msg: fmt.Sprintf("Remote %q has no installed WordPress %ss.", remoteName, kind)}
		}
		slug, err = remoteSelectFn("Choose a remote "+string(kind)+" to "+action, options)
		if err != nil {
			return envRemoteSyncTarget{}, nil, "", err
		}
	}
	if err := validatePluginSlug(slug); err != nil {
		return envRemoteSyncTarget{}, nil, "", err
	}
	return target, inventory, slug, nil
}

func remoteInventoryContains(items []remoteWordPressCode, slug string) bool {
	for _, item := range items {
		if item.Slug == slug {
			return true
		}
	}
	return false
}

func gitWorktreeClean(root string) error {
	output, err := exec.Command("git", "-C", root, "status", "--porcelain=v1", "--untracked-files=all").Output()
	if err != nil {
		return fmt.Errorf("check Git worktree: %w", err)
	}
	if strings.TrimSpace(string(output)) != "" {
		return ProjectError{Msg: "Repo pull requires a clean Git worktree, including staged and untracked files."}
	}
	return nil
}

func overlayPulledCode(sourceDir, destinationDir string) error {
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(sourceDir, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceDir, sourcePath)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		destinationPath := filepath.Join(destinationDir, rel)
		if entry.IsDir() {
			if info, err := os.Stat(destinationPath); err == nil && !info.IsDir() {
				return ProjectError{Msg: fmt.Sprintf("cannot overlay remote directory onto local file: %s", destinationPath)}
			}
			return os.MkdirAll(destinationPath, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ProjectError{Msg: fmt.Sprintf("cannot overlay unsupported remote entry: %s", sourcePath)}
		}
		if local, err := os.Stat(destinationPath); err == nil && local.IsDir() {
			return ProjectError{Msg: fmt.Sprintf("cannot overlay remote file onto local directory: %s", destinationPath)}
		}
		input, err := os.Open(sourcePath)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		outputCloseErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		return outputCloseErr
	})
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
