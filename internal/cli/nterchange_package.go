package cli

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
)

var nterchangeCommitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type nterchangePackageFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	Target string `json:"target,omitempty"`
}

type nterchangeLockedPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type"`
	Dist    struct {
		Type      string `json:"type"`
		URL       string `json:"url"`
		Reference string `json:"reference"`
	} `json:"dist"`
	Source struct {
		Type      string `json:"type"`
		URL       string `json:"url"`
		Reference string `json:"reference"`
	} `json:"source"`
}

type nterchangePackageManifest struct {
	Version      int                       `json:"version"`
	Project      string                    `json:"project"`
	Commit       string                    `json:"commit"`
	Media        []string                  `json:"excluded_media"`
	LockSHA256   string                    `json:"composer_lock_sha256"`
	Dependencies []nterchangeLockedPackage `json:"dependencies"`
	Files        []nterchangePackageFile   `json:"files"`
}

func cmdNterchangePackage(root string, current *projectMetadata, args []string) int {
	flags := flag.NewFlagSet("env package", flag.ContinueOnError)
	ref := flags.String("ref", "", "full reviewed Git commit ID (required)")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 || !nterchangeCommitID.MatchString(*ref) {
		fmt.Fprintln(os.Stderr, "Usage: nf env package --ref <full-reviewed-commit-id>")
		return 1
	}
	result, err := packageNterchange(root, current, *ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nterchange package:", err)
		return 1
	}
	fmt.Println("Package created.")
	printIndentedDetailRows([]detailRow{
		{label: "Project", value: current.Project.Slug},
		{label: "Commit", value: *ref},
		{label: "Path", value: result},
		{label: "Manifest", value: filepath.Join(result, "manifest.json")},
	}, 2)
	fmt.Println("\nNo activation performed.")
	return 0
}

// Read objects, not the index/worktree or attribute filters. Disable replacement objects and lazy fetch.
func nterchangePackageGit(root string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"--no-replace-objects", "-C", root}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1"}
	return cmd
}

func packageNterchange(root string, current *projectMetadata, ref string) (result string, err error) {
	if !nterchangeCommitID.MatchString(ref) {
		return "", fmt.Errorf("a full reviewed commit ID is required")
	}
	commit, err := nterchangePackageGit(root, "rev-parse", "--verify", ref+"^{commit}").Output()
	if err != nil || strings.TrimSpace(string(commit)) != ref {
		return "", fmt.Errorf("selected commit is unavailable: %s", ref)
	}
	parent := filepath.Join(config.DataHome(), "nterchange", "packages")
	for _, other := range []string{root, config.ConfigHome(), config.StateHome()} {
		for _, storage := range []string{config.DataHome(), parent} {
			if err := nterchangeSeparateStorage(storage, other); err != nil {
				return "", err
			}
		}
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(parent, ".building-")
	if err != nil {
		return "", err
	}
	defer func() {
		if result == "" {
			_ = os.RemoveAll(work)
		}
	}()
	code := filepath.Join(work, "code")
	if err := os.Mkdir(code, 0700); err != nil {
		return "", err
	}
	metadataBytes, err := nterchangePackageGit(root, "show", ref+":nf.json").Output()
	if err != nil {
		return "", fmt.Errorf("selected commit must contain nf.json")
	}
	if err := os.WriteFile(filepath.Join(code, "nf.json"), metadataBytes, 0600); err != nil {
		return "", err
	}
	selected, err := project.Load(code)
	if err != nil {
		return "", err
	}
	if selected.Nterchange == nil || selected.Project.Slug != current.Project.Slug || strings.Join(selected.Nterchange.MediaPaths(), "\n") != strings.Join(current.Nterchange.MediaPaths(), "\n") {
		return "", fmt.Errorf("selected commit must match the current nterchange project and media declarations")
	}
	if err := stageNterchangeCommit(root, ref, code, selected.Nterchange.MediaPaths()); err != nil {
		return "", err
	}
	for _, required := range []string{"composer.json", "composer.lock", "conf/conf.php", "public_html/index.php"} {
		info, err := os.Lstat(filepath.Join(code, required))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "", fmt.Errorf("selected commit requires a non-empty regular %s", required)
		}
	}
	if !dirExists(filepath.Join(code, "app")) {
		return "", fmt.Errorf("selected commit requires app/")
	}
	lock, err := os.ReadFile(filepath.Join(code, "composer.lock"))
	if err != nil {
		return "", err
	}
	dependencies, err := validateNterchangeComposer(code, lock)
	if err != nil {
		return "", err
	}
	fmt.Println("Installing locked production dependencies in staging. Composer scripts/plugins disabled.")
	for _, args := range [][]string{
		{"validate", "--check-lock", "--no-check-publish", "--no-check-all"},
		{"install", "--no-dev", "--prefer-dist", "--optimize-autoloader", "--no-progress"},
		{"check-platform-reqs", "--no-dev"},
	} {
		args = append(args, "--no-interaction", "--no-plugins", "--no-scripts")
		printCommandArgs(append([]string{"composer"}, args...))
		cmd := exec.Command("composer", args...)
		cmd.Dir = code
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + work, "TMPDIR=" + work,
			"COMPOSER_HOME=" + filepath.Join(work, "composer-home"), "COMPOSER_CACHE_DIR=" + filepath.Join(work, "composer-cache"),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("Composer %s failed: %w", args[0], err)
		}
	}
	after, err := os.ReadFile(filepath.Join(code, "composer.lock"))
	if err != nil || string(after) != string(lock) {
		return "", fmt.Errorf("Composer changed the staged lock; package rejected")
	}
	if err := verifyNterchangeInstalled(code, dependencies); err != nil {
		return "", err
	}
	files, err := nterchangePackageContents(code, selected.Nterchange.MediaPaths())
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(lock)
	manifest := nterchangePackageManifest{Version: 1, Project: selected.Project.Slug, Commit: ref, Media: selected.Nterchange.MediaPaths(), LockSHA256: hex.EncodeToString(sum[:]), Dependencies: dependencies, Files: files}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(work, "manifest.json"), append(data, '\n'), 0600); err != nil {
		return "", err
	}
	// Only code and the manifest survive. Composer config/cache are build inputs, not releases.
	entries, err := os.ReadDir(work)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() != "code" && entry.Name() != "manifest.json" {
			if err := os.RemoveAll(filepath.Join(work, entry.Name())); err != nil {
				return "", err
			}
		}
	}
	final := filepath.Join(parent, selected.Project.Slug+"-"+ref[:12]+"-"+strings.TrimPrefix(filepath.Base(work), ".building-"))
	if err := os.Rename(work, final); err != nil {
		return "", err
	}
	return final, nil
}

func nterchangeSeparateStorage(a, b string) error {
	resolve := func(p string) (string, error) {
		p, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		var suffix []string
		for {
			_, err := os.Lstat(p)
			if err == nil {
				real, err := filepath.EvalSymlinks(p)
				if err != nil {
					return "", err
				}
				for i := len(suffix) - 1; i >= 0; i-- {
					real = filepath.Join(real, suffix[i])
				}
				return real, nil
			}
			if !os.IsNotExist(err) || p == filepath.Dir(p) {
				return "", err
			}
			suffix = append(suffix, filepath.Base(p))
			p = filepath.Dir(p)
		}
	}
	left, err := resolve(a)
	if err != nil {
		return err
	}
	right, err := resolve(b)
	if err != nil {
		return err
	}
	if left == right || strings.HasPrefix(left, right+string(os.PathSeparator)) || strings.HasPrefix(right, left+string(os.PathSeparator)) || left == "/" || right == "/" {
		return fmt.Errorf("package data storage must be separate from checkout/config/state: %s overlaps %s", a, b)
	}
	return nil
}

// Legacy libraries are tracked under vendor; only Composer-owned generated paths are omitted.
func nterchangePackagePath(name string, media []string, generated bool) bool {
	if path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, m := range append([]string{"public_html/upload"}, media...) {
		if name == m || strings.HasPrefix(name, m+"/") {
			return false
		}
	}
	parts := strings.Split(name, "/")
	if len(parts) == 1 {
		return name == "composer.json" || name == "composer.lock" || name == "nf.json"
	}
	switch parts[0] {
	case "app", "conf", "lib", "public_html", "vendor":
	default:
		return false
	}
	if !generated && (name == "vendor/autoload.php" || name == "vendor/composer" || strings.HasPrefix(name, "vendor/composer/") || name == "vendor/nonfiction" || strings.HasPrefix(name, "vendor/nonfiction/")) {
		return false
	}
	for _, part := range parts {
		lower := strings.ToLower(part)
		if strings.HasPrefix(part, ".") && part != ".htaccess" || strings.HasPrefix(lower, "sess_") || strings.Contains(lower, ".sql.") || strings.Contains(lower, ".env.") {
			return false
		}
		switch lower {
		case "var", "cache", "caches", "tmp", "temp", "logs", "sessions", "scratch", "backups", "node_modules", "auth.json", "credentials.json", "credentials.yml", "credentials.yaml", "credentials.ini", "secrets.json", "secrets.yml", "secrets.yaml", "secrets.ini", "id_rsa", "id_ed25519":
			return false
		}
		for _, suffix := range []string{".env", ".pem", ".key", ".p12", ".pfx", ".sql", ".sql.gz", ".sql.bz2", ".sql.zip", ".sqlite", ".sqlite3", ".db", ".log", ".bak"} {
			if strings.HasSuffix(lower, suffix) {
				return false
			}
		}
	}
	return true
}

func stageNterchangeCommit(root, ref, code string, media []string) error {
	listing, err := nterchangePackageGit(root, "ls-tree", "-rz", "--full-tree", ref).Output()
	if err != nil {
		return err
	}
	cmd := nterchangePackageGit(root, "cat-file", "--batch")
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewReader(output)
	for _, entry := range strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00") {
		header, name, ok := strings.Cut(entry, "\t")
		if !ok || !nterchangePackagePath(name, media, false) {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000") {
			return fmt.Errorf("unsupported Git entry %s", name)
		}
		if _, err := fmt.Fprintln(input, fields[2]); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		object := strings.Fields(line)
		if len(object) != 3 || object[0] != fields[2] || object[1] != "blob" {
			return fmt.Errorf("invalid Git blob for %s", name)
		}
		size, err := strconv.ParseInt(object[2], 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("invalid Git blob size for %s", name)
		}
		dest := filepath.Join(code, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if fields[0] == "120000" {
			if size > 4096 {
				return fmt.Errorf("oversized source symlink %s", name)
			}
			// These two consumers use this one known in-release symlink. No directory link is followed while staging.
			target, err := io.ReadAll(io.LimitReader(reader, size))
			if err != nil || name != "public_html/nterchange" || string(target) != "../vendor/nonfiction/nterchange/nterchange" {
				return fmt.Errorf("unsupported source symlink %s", name)
			}
			if err := os.Symlink(string(target), dest); err != nil {
				return err
			}
		} else {
			mode := os.FileMode(0644)
			if fields[0] == "100755" {
				mode = 0755
			}
			f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(f, reader, size)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if b, err := reader.ReadByte(); err != nil || b != '\n' {
			return fmt.Errorf("incomplete Git blob for %s", name)
		}
	}
	if err := input.Close(); err != nil {
		return err
	}
	return cmd.Wait()
}

func validateNterchangeComposer(code string, lock []byte) ([]nterchangeLockedPackage, error) {
	data, err := os.ReadFile(filepath.Join(code, "composer.json"))
	if err != nil {
		return nil, err
	}
	var root struct {
		Config       map[string]json.RawMessage `json:"config"`
		Repositories json.RawMessage            `json:"repositories"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("invalid composer.json: %w", err)
	}
	// Existing consumers need only these settings. Other filesystem/repository overrides need separate review.
	for key := range root.Config {
		switch key {
		case "platform", "allow-plugins", "sort-packages":
		default:
			return nil, fmt.Errorf("unsupported Composer config %q for isolated packaging", key)
		}
	}
	if len(root.Repositories) != 0 && string(root.Repositories) != "[]" && string(root.Repositories) != "{}" {
		return nil, fmt.Errorf("custom Composer repositories are not supported by isolated packaging")
	}
	var locked struct {
		ContentHash string                    `json:"content-hash"`
		Packages    []nterchangeLockedPackage `json:"packages"`
	}
	if err := json.Unmarshal(lock, &locked); err != nil || locked.ContentHash == "" || locked.Packages == nil {
		return nil, fmt.Errorf("invalid composer.lock: content-hash and packages are required")
	}
	for _, dep := range locked.Packages {
		if dep.Name == "" || dep.Version == "" || (dep.Type != "library" && dep.Type != "metapackage") {
			return nil, fmt.Errorf("unsupported locked dependency %q (only libraries/metapackages are supported)", dep.Name)
		}
		if dep.Type == "metapackage" {
			continue
		}
		validURL := func(raw string) bool {
			u, err := url.Parse(raw)
			return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
		}
		if dep.Dist.Type != "zip" || !validURL(dep.Dist.URL) || !nterchangeCommitID.MatchString(dep.Dist.Reference) {
			return nil, fmt.Errorf("locked dependency %s requires an HTTPS zip and full commit reference", dep.Name)
		}
		if dep.Source.Type != "" && (dep.Source.Type != "git" || !validURL(dep.Source.URL) || dep.Source.Reference != dep.Dist.Reference) {
			return nil, fmt.Errorf("unsafe source fallback for locked dependency %s", dep.Name)
		}
	}
	return locked.Packages, nil
}

func verifyNterchangeInstalled(code string, locked []nterchangeLockedPackage) error {
	if !fileExists(filepath.Join(code, "vendor", "autoload.php")) {
		return fmt.Errorf("Composer did not produce vendor/autoload.php")
	}
	data, err := os.ReadFile(filepath.Join(code, "vendor", "composer", "installed.json"))
	if err != nil {
		return err
	}
	var installed struct {
		Dev      bool                      `json:"dev"`
		Packages []nterchangeLockedPackage `json:"packages"`
	}
	if err := json.Unmarshal(data, &installed); err != nil {
		return err
	}
	if installed.Dev || len(installed.Packages) != len(locked) {
		return fmt.Errorf("installed dependency set does not match production lock")
	}
	for _, dep := range locked {
		found := false
		for _, actual := range installed.Packages {
			if actual.Name == dep.Name && actual.Version == dep.Version && actual.Dist.Reference == dep.Dist.Reference && actual.Source.Reference == dep.Source.Reference {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("installed dependency does not match lock: %s", dep.Name)
		}
	}
	return nil
}

func nterchangePackageContents(code string, media []string) ([]nterchangePackageFile, error) {
	canonicalCode, err := filepath.EvalSymlinks(code)
	if err != nil {
		return nil, err
	}
	var files []nterchangePackageFile
	err = filepath.WalkDir(code, func(full string, entry os.DirEntry, err error) error {
		if err != nil || full == code {
			return err
		}
		rel, err := filepath.Rel(code, full)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		// Top-level runtime code directories are allowed even though they are not files.
		allowed := nterchangePackagePath(name, media, true)
		if entry.IsDir() && !strings.Contains(name, "/") {
			allowed = name == "app" || name == "conf" || name == "lib" || name == "public_html" || name == "vendor"
		}
		if !allowed {
			if err := os.RemoveAll(full); err != nil {
				return err
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file := nterchangePackageFile{Path: name, Mode: fmt.Sprintf("%04o", info.Mode().Perm())}
		var sum [32]byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(full)
			if err != nil || filepath.IsAbs(target) || !strings.HasPrefix(resolved, canonicalCode+string(os.PathSeparator)) {
				return fmt.Errorf("unsafe or missing package symlink %s", name)
			}
			relTarget, err := filepath.Rel(canonicalCode, resolved)
			if err != nil || !nterchangePackagePath(filepath.ToSlash(relTarget), media, true) {
				return fmt.Errorf("package symlink targets excluded data: %s", name)
			}
			file.Target = target
			file.Size = int64(len(target))
			sum = sha256.Sum256([]byte(target))
		} else if info.Mode().IsRegular() {
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			h := sha256.New()
			file.Size, err = io.Copy(h, f)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			copy(sum[:], h.Sum(nil))
		} else {
			return fmt.Errorf("unsupported package file %s", name)
		}
		file.SHA256 = hex.EncodeToString(sum[:])
		files = append(files, file)
		return nil
	})
	return files, err
}
