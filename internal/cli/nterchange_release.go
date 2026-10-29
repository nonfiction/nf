package cli

import (
	"archive/tar"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
)

//go:embed nterchange_release.py
var nterchangeReleaseScript string

func verifyNterchangePackage(location string, current *projectMetadata) (nterchangePackageManifest, error) {
	var manifest nterchangePackageManifest
	info, err := os.Lstat(location)
	if err != nil || !info.IsDir() {
		return manifest, fmt.Errorf("package must be a real directory")
	}
	data, err := os.ReadFile(filepath.Join(location, "manifest.json"))
	if err != nil {
		return manifest, err
	}
	if err := decodeSSHJSON(data, &manifest); err != nil {
		return manifest, err
	}
	if manifest.Version != 1 || !nterchangeCommitID.MatchString(manifest.Commit) || manifest.Project != current.Project.Slug || !reflect.DeepEqual(manifest.Media, current.Nterchange.MediaPaths()) {
		return manifest, fmt.Errorf("package identity/media do not match this project")
	}
	code := filepath.Join(location, "code")
	info, err = os.Lstat(code)
	if err != nil || !info.IsDir() {
		return manifest, fmt.Errorf("package code must be a real directory")
	}
	expected := map[string]nterchangePackageFile{}
	for _, file := range manifest.Files {
		if file.Target == "" && file.Mode != "0600" && file.Mode != "0644" && file.Mode != "0755" {
			return manifest, fmt.Errorf("unsupported package mode: %s", file.Path)
		}
		if !nterchangePackagePath(file.Path, manifest.Media, true) {
			return manifest, fmt.Errorf("excluded package path %q", file.Path)
		}
		if _, exists := expected[file.Path]; exists {
			return manifest, fmt.Errorf("duplicate package path %q", file.Path)
		}
		expected[file.Path] = file
	}
	count := 0
	err = filepath.WalkDir(code, func(full string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if full == code || entry.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(code, full)
		name := filepath.ToSlash(rel)
		file, ok := expected[name]
		if !ok {
			return fmt.Errorf("unlisted package file %q", name)
		}
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		var data []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(full)
			canonical, canonicalErr := filepath.EvalSymlinks(code)
			if err != nil || canonicalErr != nil || filepath.IsAbs(target) || !strings.HasPrefix(resolved, canonical+string(os.PathSeparator)) || file.Target != target {
				return fmt.Errorf("unsafe package link %q", name)
			}
			data = []byte(target)
		} else if info.Mode().IsRegular() && file.Target == "" {
			data, err = os.ReadFile(full)
			if err != nil {
				return err
			}
		} else {
			return fmt.Errorf("unsupported package file %q", name)
		}
		sum := sha256.Sum256(data)
		if file.Size != int64(len(data)) || file.SHA256 != hex.EncodeToString(sum[:]) || file.Mode != fmt.Sprintf("%04o", info.Mode().Perm()) {
			return fmt.Errorf("package integrity mismatch: %s", name)
		}
		count++
		return nil
	})
	if err != nil {
		return manifest, err
	}
	if count != len(expected) {
		return manifest, fmt.Errorf("package has missing files")
	}
	for _, name := range []string{"nf.json", "composer.json", "composer.lock", "conf/conf.php", "public_html/index.php", "vendor/autoload.php"} {
		file, ok := expected[name]
		if !ok || file.Target != "" || file.Size == 0 {
			return manifest, fmt.Errorf("package requires regular %s", name)
		}
	}
	selected, err := project.Load(code)
	if err != nil || selected.Nterchange == nil || selected.Project.Slug != manifest.Project || !reflect.DeepEqual(selected.Nterchange.MediaPaths(), manifest.Media) {
		return manifest, fmt.Errorf("packaged nf.json does not match manifest")
	}
	lock, err := os.ReadFile(filepath.Join(code, "composer.lock"))
	if err != nil {
		return manifest, err
	}
	sum := sha256.Sum256(lock)
	deps, err := validateNterchangeComposer(code, lock)
	if err != nil {
		return manifest, err
	}
	if manifest.LockSHA256 != hex.EncodeToString(sum[:]) || !reflect.DeepEqual(deps, manifest.Dependencies) {
		return manifest, fmt.Errorf("package lock/dependency metadata differ")
	}
	return manifest, verifyNterchangeInstalled(code, deps)
}

// Use the release worker's read-only verifier so pull and activation agree on code ownership.
func inspectNterchangeRemoteRoot(target sshTarget, root, compose, service string, media []string, database string, app nterchangeContainer) (string, error) {
	mounted := ""
	for _, mount := range app.Mounts {
		if mount.Destination == "/var/www/site" && mount.Type == "bind" && !mount.RW {
			mounted = mount.Source
		}
	}
	request, err := json.Marshal(map[string]any{"root": root, "compose": compose, "service": service, "media": media, "database": database, "mounted": mounted, "image": app.Image})
	if err != nil {
		return "", err
	}
	args := append(sshTargetArgs(target), renderCommandArgs([]string{"sudo", "-n", "python3", "-c", nterchangeReleaseScript, "--inspect-root", string(request)}))
	printCommandArgs(append(sshTargetArgs(target), "sudo -n python3 <nf read-only code ownership check>"))
	data, err := runSSHOutputFn(args)
	if err != nil {
		return "", fmt.Errorf("remote code ownership check: %w", err)
	}
	var result struct {
		Root    string `json:"root"`
		Managed bool   `json:"managed"`
	}
	if err := decodeSSHJSON(data, &result); err != nil || result.Root == "" {
		return "", fmt.Errorf("invalid remote code ownership result")
	}
	return result.Root, nil
}

type nterchangeReleaseRequest struct {
	Operation string                     `json:"operation"`
	Site      sshSiteEnv                 `json:"site"`
	Manifest  *nterchangePackageManifest `json:"manifest,omitempty"`
}

var nterchangeReleaseRunFn = func(target sshTarget, input *os.File) error {
	args := append(sshTargetArgs(target), renderCommandArgs([]string{"sudo", "-n", "python3", "-c", nterchangeReleaseScript}))
	// The embedded program is fixed, not a user-supplied remote shell command.
	printCommandArgs(append(sshTargetArgs(target), "sudo -n python3 <nf release worker>"))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, os.Stdout, os.Stderr
	return cmd.Run()
}

func cmdNterchangeRelease(root string, metadata *projectMetadata, operation string, args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "Usage: nf env deploy <remote> --package <directory> [--execute --yes] or nf env rollback <remote> [--execute --yes]")
		return 1
	}
	remote := args[0]
	flags := flag.NewFlagSet("env "+operation, flag.ContinueOnError)
	location := flags.String("package", "", "previously reviewed package directory")
	execute := flags.Bool("execute", false, "recreate only the selected app service, with health recovery")
	yes := flags.Bool("yes", false, "confirm execution without prompting")
	dryRun := flags.Bool("dry-run", false, "connection-free plan")
	if err := flags.Parse(args[1:]); err != nil {
		return 1
	}
	if flags.NArg() != 0 || (*yes && !*execute) || (*execute && *dryRun) || (operation == "rollback" && *location != "") || (operation == "deploy" && *location == "") {
		fmt.Fprintln(os.Stderr, "Choose a package for deploy only, and use --execute [--yes] or --dry-run, not both.")
		return 1
	}
	target, err := resolveNterchangePullTarget(metadata, remote)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	request := nterchangeReleaseRequest{Operation: operation, Site: target.Site}
	if operation == "deploy" {
		absolute, err := filepath.Abs(*location)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		*location = absolute
		manifest, err := verifyNterchangePackage(*location, metadata)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Package rejected:", err)
			return 1
		}
		request.Manifest = &manifest
	}
	fmt.Printf("Env %s plan:\n", operation)
	rows := []detailRow{
		{label: "remote:", value: remote},
		{label: "site:", value: target.Site.Name + "." + target.SSH.Name},
		{label: "env:", value: target.Site.Env},
		{label: "provider:", value: "ssh"},
		{label: "target:", value: target.SSH.Name},
		{label: "url:", value: target.Site.URL},
		{label: "path:", value: target.Site.Path},
		{label: "compose:", value: target.Site.ComposeFile},
		{label: "app service:", value: target.Site.Service},
		{label: "preserve:", value: "database, credentials, media, var, base Compose and original source tree"},
	}
	if request.Manifest != nil {
		rows = append(rows, detailRow{label: "commit:", value: request.Manifest.Commit}, detailRow{label: "package:", value: *location})
	}
	mode := "dry-run"
	if *execute {
		mode = "execute"
	}
	rows = append(rows, detailRow{label: "mode:", value: mode})
	printIndentedDetailRows(rows, 2)
	if !*execute {
		fmt.Println("Plan only. No SSH, writes, health requests or container changes.")
		return 0
	}
	if !*yes {
		confirmed, err := envRemoteSyncConfirm("Recreate only this app service and verify container-local HTTP? Failure restores previous code; failed recovery leaves a journal for manual repair.", false)
		if err != nil || !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted; no connection or change.")
			return 1
		}
	}
	parent := filepath.Join(config.DataHome(), "nterchange", "transfers")
	for _, other := range []string{root, config.ConfigHome(), config.StateHome(), *location} {
		if other == "" {
			continue
		}
		if err := nterchangeSeparateStorage(parent, other); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	input, err := os.CreateTemp(parent, ".release-*.tar")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.Remove(input.Name())
	defer input.Close()
	if err := writeNterchangeReleaseInput(input, request, *location); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := nterchangeReleaseRunFn(target.SSH, input); err != nil {
		fmt.Fprintln(os.Stderr, "Release operation failed:", err)
		return 1
	}
	return 0
}

func writeNterchangeReleaseInput(w io.Writer, request nterchangeReleaseRequest, location string) error {
	if err := json.NewEncoder(w).Encode(request); err != nil {
		return err
	}
	if request.Manifest == nil {
		return nil
	}
	archive := tar.NewWriter(w)
	for _, file := range request.Manifest.Files {
		mode, err := strconv.ParseInt(file.Mode, 8, 64)
		if err != nil || (mode != 0600 && mode != 0644 && mode != 0755 && file.Target == "") {
			return fmt.Errorf("unsupported package mode: %s", file.Path)
		}
		header := &tar.Header{Name: file.Path, Mode: mode, Size: file.Size, Typeflag: tar.TypeReg}
		if file.Target != "" {
			header.Typeflag, header.Linkname, header.Size = tar.TypeSymlink, file.Target, 0
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if file.Target == "" {
			data, err := os.ReadFile(filepath.Join(location, "code", filepath.FromSlash(file.Path)))
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			if int64(len(data)) != file.Size || hex.EncodeToString(sum[:]) != file.SHA256 {
				return fmt.Errorf("package changed during upload: %s", file.Path)
			}
			if _, err := archive.Write(data); err != nil {
				return err
			}
		}
	}
	return archive.Close()
}
