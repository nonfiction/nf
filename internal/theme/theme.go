package theme

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nonfiction/nf/internal/config"
)

type ThemeError struct{ Msg string }

func (e ThemeError) Error() string { return e.Msg }

func LoadProjectMetadata(root string) (map[string]any, error) {
	path := config.ProjectFile(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	obj, ok := payload.(map[string]any)
	if !ok {
		return nil, ThemeError{Msg: fmt.Sprintf("%s must contain a JSON object", path)}
	}
	return obj, nil
}

var excludedNames = map[string]struct{}{
	".git": {}, ".github": {}, ".DS_Store": {}, ".cache": {}, ".idea": {}, ".vscode": {}, "node_modules": {},
}

var excludedFiles = map[string]struct{}{
	".editorconfig":          {},
	".eslintignore":          {},
	".eslintrc":              {},
	".eslintrc.cjs":          {},
	".eslintrc.js":           {},
	".eslintrc.json":         {},
	".php-cs-fixer.cache":    {},
	".php-cs-fixer.dist.php": {},
	".php-cs-fixer.php":      {},
	".prettierignore":        {},
	".prettierrc":            {},
	".prettierrc.json":       {},
	"composer.json":          {},
	"composer.lock":          {},
	"eslint.config.js":       {},
	"package-lock.json":      {},
	"package.json":           {},
	"phpcs.xml":              {},
	"phpcs.xml.dist":         {},
	"phpstan.neon":           {},
	"phpstan.neon.dist":      {},
	"pnpm-lock.yaml":         {},
	"psalm.xml":              {},
	"tsconfig.json":          {},
	"yarn.lock":              {},
}

var excludedFilePrefixes = []string{
	"postcss.config.",
	"prettier.config.",
	"tailwind.config.",
	"vite.config.",
	"webpack.config.",
}

func shouldSkip(path, outputPath string) bool {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for _, part := range parts {
		if _, ok := excludedNames[part]; ok {
			return true
		}
	}
	if outputPath != "" {
		pathAbs, err1 := filepath.Abs(path)
		outputAbs, err2 := filepath.Abs(outputPath)
		if err1 == nil && err2 == nil && pathAbs == outputAbs {
			return true
		}
	}
	base := filepath.Base(path)
	if _, ok := excludedFiles[base]; ok {
		return true
	}
	for _, prefix := range excludedFilePrefixes {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

func shouldSkipSourceCopy(sourceRoot, path, outputPath string, composerTheme bool) bool {
	if composerTheme {
		base := filepath.Base(path)
		if base == "composer.json" || base == "composer.lock" {
			if outputPath == "" {
				return false
			}
			pathAbs, err1 := filepath.Abs(path)
			outputAbs, err2 := filepath.Abs(outputPath)
			return err1 == nil && err2 == nil && pathAbs == outputAbs
		}
	}
	if shouldSkip(path, outputPath) {
		return true
	}
	if !composerTheme {
		return false
	}
	rel, err := filepath.Rel(sourceRoot, path)
	if err != nil {
		return false
	}
	first := strings.Split(filepath.ToSlash(rel), "/")[0]
	return first == "vendor"
}

func archiveName(root, path, archiveRoot string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(archiveRoot, rel)), nil
}

type Result struct {
	SourceDir   string
	OutputPath  string
	ArchiveRoot string
	FileCount   int
	DryRun      bool
}

func PackageTheme(sourceDir, outputPath, archiveRoot string, dryRun bool) (Result, error) {
	info, err := os.Stat(sourceDir)
	if err != nil || !info.IsDir() {
		return Result{}, ThemeError{Msg: fmt.Sprintf("Theme source directory does not exist: %s", sourceDir)}
	}
	archiveRoot = strings.TrimSpace(archiveRoot)
	if archiveRoot == "" {
		archiveRoot = filepath.Base(filepath.Clean(sourceDir))
	}
	if filepath.IsAbs(archiveRoot) || strings.ContainsAny(archiveRoot, "/\\") || strings.Contains(archiveRoot, "..") {
		return Result{}, ThemeError{Msg: fmt.Sprintf("Theme archive root %q must be one safe directory name", archiveRoot)}
	}
	if err := validateBuildOutput(sourceDir); err != nil {
		return Result{}, err
	}
	packageRoot := sourceDir
	if !dryRun {
		stagedRoot, err := stageTheme(sourceDir, outputPath)
		if err != nil {
			return Result{}, err
		}
		packageRoot = stagedRoot
		defer func() { _ = os.RemoveAll(stagedRoot) }()
	}
	files, err := packageFiles(packageRoot, outputPath)
	if err != nil {
		return Result{}, err
	}
	if !dryRun {
		if err := writeZip(packageRoot, outputPath, archiveRoot, files); err != nil {
			return Result{}, err
		}
	}
	return Result{SourceDir: sourceDir, OutputPath: outputPath, ArchiveRoot: archiveRoot, FileCount: len(files), DryRun: dryRun}, nil
}

func packageFiles(root, outputPath string) ([]string, error) {
	files := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if shouldSkip(path, outputPath) {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldSkip(path, outputPath) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func writeZip(root, outputPath, archiveRoot string, files []string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	for _, path := range files {
		name, err := archiveName(root, path, archiveRoot)
		if err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
		w, err := zw.Create(name)
		if err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
		if _, err := w.Write(data); err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func stageTheme(sourceDir, outputPath string) (string, error) {
	stageRoot, err := os.MkdirTemp("", "nf-theme-package-*")
	if err != nil {
		return "", err
	}
	composerTheme := fileExists(filepath.Join(sourceDir, "composer.json"))
	if err := copyThemeSource(sourceDir, stageRoot, outputPath, composerTheme); err != nil {
		_ = os.RemoveAll(stageRoot)
		return "", err
	}
	if composerTheme {
		if err := runComposerInstall(stageRoot); err != nil {
			_ = os.RemoveAll(stageRoot)
			return "", err
		}
	}
	return stageRoot, nil
}

func copyThemeSource(sourceDir, stageRoot, outputPath string, composerTheme bool) error {
	return filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceDir {
			return nil
		}
		if d.IsDir() {
			if shouldSkipSourceCopy(sourceDir, path, outputPath, composerTheme) {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(sourceDir, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(filepath.Join(stageRoot, rel), info.Mode().Perm())
		}
		if shouldSkipSourceCopy(sourceDir, path, outputPath, composerTheme) {
			return nil
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		return copyFile(path, filepath.Join(stageRoot, rel), d)
	})
}

func copyFile(sourcePath, destPath string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func runComposerInstall(stageRoot string) error {
	cmd := exec.Command("composer", "install", "--no-dev", "--no-interaction", "--prefer-dist", "--optimize-autoloader", "--no-progress")
	cmd.Dir = stageRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if text != "" {
			return ThemeError{Msg: fmt.Sprintf("composer install --no-dev failed while staging theme package: %v\n%s", err, text)}
		}
		return ThemeError{Msg: fmt.Sprintf("composer install --no-dev failed while staging theme package: %v", err)}
	}
	if !fileExists(filepath.Join(stageRoot, "vendor", "autoload.php")) {
		return ThemeError{Msg: "composer install --no-dev did not create vendor/autoload.php in the staged theme package"}
	}
	return nil
}

func validateBuildOutput(sourceDir string) error {
	packagePath := filepath.Join(sourceDir, "package.json")
	data, err := os.ReadFile(packagePath)
	if err != nil {
		return nil
	}
	var payload struct {
		Scripts map[string]any `json:"scripts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	if strings.TrimSpace(jsonString(payload.Scripts["build"])) == "" {
		return nil
	}
	for _, rel := range []string{"dist", filepath.Join("assets", "dist")} {
		if dirHasFiles(filepath.Join(sourceDir, rel)) {
			return nil
		}
	}
	return ThemeError{Msg: fmt.Sprintf("theme build output missing: %s has a build script, but neither dist/ nor assets/dist/ contains files; run nf theme build before packaging", packagePath)}
}

func jsonString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirHasFiles(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if dirHasFiles(filepath.Join(path, entry.Name())) {
				return true
			}
			continue
		}
		if entry.Name() != ".gitkeep" {
			return true
		}
	}
	return false
}
