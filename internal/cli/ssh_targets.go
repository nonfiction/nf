package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
)

// SSH targets are registered connections, not disposable provider inventory.
type sshTarget struct {
	Name           string `json:"name"`
	Host           string `json:"host"`
	User           string `json:"user"`
	Port           int    `json:"port"`
	IdentityFile   string `json:"identity_file,omitempty"`
	KnownHostsFile string `json:"known_hosts_file,omitempty"`
}

func loadSSHTargets() ([]sshTarget, error) {
	data, err := os.ReadFile(filepath.Join(config.ConfigHome(), "ssh-targets.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var targets []sshTarget
	if err := decodeSSHJSON(data, &targets); err != nil {
		return nil, fmt.Errorf("read ssh-targets.json: %w", err)
	}
	if targets == nil {
		return nil, fmt.Errorf("ssh-targets.json must contain an array")
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if err := target.validate(); err != nil {
			return nil, err
		}
		key := strings.ToLower(target.Name)
		if seen[key] {
			return nil, fmt.Errorf("duplicate SSH target %q", target.Name)
		}
		seen[key] = true
	}
	return targets, nil
}

func decodeSSHJSON(data []byte, value any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected data after JSON document")
	}
	return nil
}

func (target sshTarget) validate() error {
	if err := project.ValidateName(target.Name); err != nil {
		return fmt.Errorf("SSH target name: %w", err)
	}
	if strings.HasPrefix(target.Name, "-") {
		return fmt.Errorf("SSH target name must not start with a hyphen")
	}
	if !safeSSHName(target.Host, true) || !safeSSHName(target.User, false) {
		return fmt.Errorf("SSH target requires a safe host and user")
	}
	if target.Port < 1 || target.Port > 65535 {
		return fmt.Errorf("SSH port must be between 1 and 65535")
	}
	for _, file := range []string{target.IdentityFile, target.KnownHostsFile} {
		if file != "" && (!filepath.IsAbs(file) || strings.ContainsAny(file, "\r\n\x00")) {
			return fmt.Errorf("SSH identity and known-hosts paths must be absolute paths without line breaks")
		}
	}
	return nil
}

func safeSSHName(value string, host bool) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, c := range value {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("._-", c) || (host && c == ':') {
			continue
		}
		return false
	}
	return true
}

func (target sshTarget) record() map[string]any {
	return map[string]any{
		"provider": "ssh", "target_name": target.Name, "hostname": target.Host, "status": "registered",
		"ssh": map[string]any{"host": target.Host, "user": target.User, "port": strconv.Itoa(target.Port), "identity_file": target.IdentityFile, "known_hosts_file": target.KnownHostsFile},
	}
}

func appendSSHTargets(existing []map[string]any) ([]map[string]any, error) {
	registered, err := loadSSHTargets()
	if err != nil {
		return nil, err
	}
	for _, target := range registered {
		if record := matchingTargetByName(existing, target.Name); record != nil {
			return nil, fmt.Errorf("SSH target %q conflicts with provider inventory", target.Name)
		}
		existing = append(existing, target.record())
	}
	return existing, nil
}

func matchingTargetByName(targets []map[string]any, name string) map[string]any {
	for _, target := range targets {
		if strings.EqualFold(firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"), name) {
			return target
		}
	}
	return nil
}

func runAddSSHTarget(argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nf target add ssh <name> --host <host> [--user nonfiction]")
		return 1
	}
	if argv[0] == "--help" || argv[0] == "-h" || argv[0] == "help" {
		return runTargetHelp()
	}
	target := sshTarget{Name: argv[0]}
	flags := flag.NewFlagSet("target add ssh", flag.ContinueOnError)
	flags.StringVar(&target.Host, "host", "", "existing SSH host; no infrastructure is provisioned")
	flags.StringVar(&target.User, "user", "nonfiction", "SSH user")
	flags.IntVar(&target.Port, "port", 22, "SSH port")
	flags.StringVar(&target.IdentityFile, "identity-file", "", "existing private-key path; contents are never copied")
	flags.StringVar(&target.KnownHostsFile, "known-hosts-file", "", "existing host-trust file; defaults to OpenSSH configuration")
	if err := flags.Parse(argv[1:]); err != nil {
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected target add ssh arguments")
		return 1
	}
	for _, file := range []*string{&target.IdentityFile, &target.KnownHostsFile} {
		if *file == "" {
			continue
		}
		resolved, err := filepath.Abs(*file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		*file = resolved
		if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() {
			fmt.Fprintln(os.Stderr, "SSH identity/known-hosts file must already exist and be a regular file")
			return 1
		}
	}
	if err := target.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	existing, err := cachedTargets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if matchingTargetByName(existing, target.Name) != nil {
		fmt.Fprintf(os.Stderr, "Target %q already exists; registration was not changed.\n", target.Name)
		return 1
	}
	targets, err := loadSSHTargets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	targets = append(targets, target)
	data, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(config.ConfigHome(), 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	file, err := os.CreateTemp(config.ConfigHome(), ".ssh-targets-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), filepath.Join(config.ConfigHome(), "ssh-targets.json"))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Registered existing SSH target %s. No connection or server change was made.\n", target.Name)
	return 0
}

func sshTargetArgs(target sshTarget) []string {
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes"}
	args = append(args, "-p", strconv.Itoa(target.Port))
	if target.IdentityFile != "" {
		args = append(args, "-o", "IdentitiesOnly=yes", "-i", target.IdentityFile)
	}
	if target.KnownHostsFile != "" {
		args = append(args, "-o", "UserKnownHostsFile="+target.KnownHostsFile)
	}
	return append(args, "--", target.User+"@"+target.Host)
}

// Only non-secret application facts belong in /var/lib/nf/sites.json.
type sshSiteEnv struct {
	Name        string   `json:"name"`
	Env         string   `json:"env"`
	Kind        string   `json:"kind"`
	URL         string   `json:"url"`
	Path        string   `json:"path"`
	ComposeFile string   `json:"compose_file"`
	Service     string   `json:"service"`
	Database    string   `json:"database"`
	Media       []string `json:"media,omitempty"`
}

func (site sshSiteEnv) validate() error {
	for _, name := range []string{site.Name, site.Env, site.Service, site.Database} {
		if err := project.ValidateName(name); err != nil || strings.TrimSpace(name) != name || strings.HasPrefix(name, "-") {
			return fmt.Errorf("SSH site requires safe name, env, service and database names")
		}
	}
	if site.Kind != "nterchange" {
		return fmt.Errorf("SSH site kind must be nterchange")
	}
	for _, location := range []string{site.Path, site.ComposeFile} {
		if !path.IsAbs(location) || path.Clean(location) != location || location == "/" || strings.ContainsAny(location, "\r\n\x00") {
			return fmt.Errorf("SSH site paths must be clean absolute paths below root")
		}
	}
	u, err := url.Parse(site.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("SSH site URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	return (&project.Nterchange{Media: site.Media}).Validate()
}

func discoverSSHTargetSites(record map[string]any) ([]map[string]any, error) {
	targets, err := loadSSHTargets()
	if err != nil {
		return nil, err
	}
	name := firstRecordString(record, "target_name")
	var target sshTarget
	found := false
	for _, registered := range targets {
		if registered.Name == name {
			target = registered
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("SSH target %q is not registered", name)
	}
	args := append(sshTargetArgs(target), "head", "-c", "1048577", "/var/lib/nf/sites.json")
	data, err := runSSHOutputFn(args)
	if err != nil {
		return nil, err
	}
	if len(data) > 1048576 {
		return nil, fmt.Errorf("SSH site inventory exceeds 1 MiB")
	}
	var sites []sshSiteEnv
	if err := decodeSSHJSON(data, &sites); err != nil {
		return nil, fmt.Errorf("read SSH site inventory: %w", err)
	}
	if sites == nil {
		return nil, fmt.Errorf("SSH site inventory must contain an array")
	}
	seen := map[string]bool{}
	records := make([]map[string]any, 0, len(sites))
	for _, site := range sites {
		if err := site.validate(); err != nil {
			return nil, err
		}
		siteID := site.Name + "." + name
		envID := siteID + ":" + site.Env
		if seen[envID] {
			return nil, fmt.Errorf("duplicate SSH site env %q", envID)
		}
		seen[envID] = true
		records = append(records, map[string]any{
			"provider": "ssh", "target": name, "site_id": siteID, "env_id": envID, "name": site.Name, "project_slug": site.Name, "env": site.Env, "kind": site.Kind, "url": site.URL, "path": site.Path,
			"ssh":        target.record()["ssh"],
			"nterchange": map[string]any{"compose_file": site.ComposeFile, "service": site.Service, "database": site.Database, "media": (&project.Nterchange{Media: site.Media}).MediaPaths()},
		})
	}
	return records, nil
}
