package theme

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
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

var excludedNames = map[string]struct{}{".git": {}, ".DS_Store": {}, "node_modules": {}}

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
	return false
}

func archiveName(root, path string) (string, error) {
	return filepath.Rel(filepath.Dir(root), path)
}

type Result struct {
	SourceDir  string
	OutputPath string
	FileCount  int
	DryRun     bool
}

func PackageTheme(sourceDir, outputPath string, dryRun bool) (Result, error) {
	info, err := os.Stat(sourceDir)
	if err != nil || !info.IsDir() {
		return Result{}, ThemeError{Msg: fmt.Sprintf("Theme source directory does not exist: %s", sourceDir)}
	}
	files := make([]string, 0)
	err = filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, walkErr error) error {
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
		return Result{}, err
	}
	sort.Strings(files)
	if !dryRun {
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return Result{}, err
		}
		out, err := os.Create(outputPath)
		if err != nil {
			return Result{}, err
		}
		zw := zip.NewWriter(out)
		for _, path := range files {
			name, err := archiveName(sourceDir, path)
			if err != nil {
				_ = zw.Close()
				_ = out.Close()
				return Result{}, err
			}
			w, err := zw.Create(name)
			if err != nil {
				_ = zw.Close()
				_ = out.Close()
				return Result{}, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				_ = zw.Close()
				_ = out.Close()
				return Result{}, err
			}
			if _, err := w.Write(data); err != nil {
				_ = zw.Close()
				_ = out.Close()
				return Result{}, err
			}
		}
		if err := zw.Close(); err != nil {
			_ = out.Close()
			return Result{}, err
		}
		if err := out.Close(); err != nil {
			return Result{}, err
		}
	}
	return Result{SourceDir: sourceDir, OutputPath: outputPath, FileCount: len(files), DryRun: dryRun}, nil
}
