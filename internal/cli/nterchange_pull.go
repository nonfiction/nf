package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
)

// Preflight commands inspect Docker and database metadata, never boot the CMS.
var nterchangeOutputFn = func(dir string, args []string) ([]byte, error) {
	printCommandArgs(args)
	return runCommandSpecStdinOutputSilent(execSpec{Dir: dir, Args: args}, "")
}

type nterchangePullTarget struct {
	SSH  sshTarget
	Site sshSiteEnv
}

type nterchangeContainer struct {
	ID, Image string
	State     struct {
		Status string
		Paused bool
		Health *struct{ Status string }
	}
	Config struct{ Env []string }
	Mounts []struct {
		Type, Source, Destination string
		RW                        bool
	}
	NetworkSettings struct {
		Networks map[string]struct{ NetworkID string }
	}
}

type nterchangeDatabaseFacts struct {
	Charset, Collation string
	Engines            []string
}

type nterchangeRuntimeFacts struct {
	AppID, DBID string
	Database    string
	Storage     string
	SQL         nterchangeDatabaseFacts
}

func resolveNterchangePullTarget(metadata *projectMetadata, remote string) (nterchangePullTarget, error) {
	siteID, env, ok, err := projectRemoteAlias(metadata, remote)
	if err != nil {
		return nterchangePullTarget{}, err
	}
	if !ok {
		return nterchangePullTarget{}, fmt.Errorf("nterchange pull requires an explicit configured remote; no remote named %q", remote)
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		return nterchangePullTarget{}, err
	}
	if record == nil || firstRecordString(record, "provider") != "ssh" || firstRecordString(record, "kind") != "nterchange" {
		return nterchangePullTarget{}, fmt.Errorf("nterchange pull requires a cached nterchange env on a registered SSH target; run nf site refresh")
	}
	nf, _ := record["nterchange"].(map[string]any)
	mediaJSON, err := json.Marshal(nf["media"])
	if err != nil {
		return nterchangePullTarget{}, err
	}
	var media []string
	if err := json.Unmarshal(mediaJSON, &media); err != nil {
		return nterchangePullTarget{}, fmt.Errorf("invalid cached nterchange media")
	}
	site := sshSiteEnv{
		Name: firstRecordString(record, "name"), Env: env, Kind: "nterchange", URL: firstRecordString(record, "url"), Path: firstRecordString(record, "path"),
		ComposeFile: firstRecordString(nf, "compose_file"), Service: firstRecordString(nf, "service"), Database: firstRecordString(nf, "database"), Media: media,
	}
	if err := site.validate(); err != nil {
		return nterchangePullTarget{}, err
	}
	if site.Name != metadata.Project.Slug || siteID != site.Name+"."+firstRecordString(record, "target") {
		return nterchangePullTarget{}, fmt.Errorf("remote nterchange site does not match project %q", metadata.Project.Slug)
	}
	if !reflect.DeepEqual(metadata.Nterchange.MediaPaths(), (&project.Nterchange{Media: media}).MediaPaths()) {
		return nterchangePullTarget{}, fmt.Errorf("local and remote nterchange media declarations differ; reconcile nf.json before pulling")
	}
	targets, err := loadSSHTargets()
	if err != nil {
		return nterchangePullTarget{}, err
	}
	for _, target := range targets {
		if target.Name == firstRecordString(record, "target") {
			return nterchangePullTarget{SSH: target, Site: site}, nil
		}
	}
	return nterchangePullTarget{}, fmt.Errorf("remote SSH target is no longer registered")
}

func nterchangeRemoteOutput(target sshTarget, args ...string) ([]byte, error) {
	command := append(sshTargetArgs(target), renderCommandArgs(append([]string{"sudo", "-n"}, args...)))
	printCommandArgs(command)
	return runSSHOutputFn(command)
}

func inspectNterchangeRuntime(root, compose, appService string, media []string, database string, target *sshTarget) (nterchangeRuntimeFacts, error) {
	var facts nterchangeRuntimeFacts
	output := func(args ...string) ([]byte, error) {
		if target != nil {
			return nterchangeRemoteOutput(*target, args...)
		}
		return nterchangeOutputFn(root, args)
	}
	containers := make([]nterchangeContainer, 0, 2)
	for _, service := range []string{appService, "db"} {
		ids, err := output("docker", "compose", "-f", compose, "ps", "-q", service)
		if err != nil {
			return facts, fmt.Errorf("inspect %s service: %w", service, err)
		}
		if len(strings.Fields(string(ids))) != 1 {
			return facts, fmt.Errorf("nterchange preflight requires exactly one running %s container", service)
		}
		data, err := output("docker", "inspect", strings.TrimSpace(string(ids)))
		if err != nil {
			return facts, fmt.Errorf("inspect %s container: %w", service, err)
		}
		var records []nterchangeContainer
		if err := json.Unmarshal(data, &records); err != nil || len(records) != 1 {
			return facts, fmt.Errorf("invalid Docker inspection for %s", service)
		}
		c := records[0]
		if c.State.Status != "running" || c.State.Paused || (c.State.Health != nil && c.State.Health.Status != "healthy") {
			return facts, fmt.Errorf("%s container is not running and healthy", service)
		}
		containers = append(containers, c)
	}
	app, db := containers[0], containers[1]
	facts.AppID, facts.DBID = app.ID, db.ID
	mount := func(c nterchangeContainer, destination, source string, writable bool) bool {
		for _, m := range c.Mounts {
			if m.Destination == destination {
				return m.Type == "bind" && m.Source == source && m.RW == writable
			}
		}
		return false
	}
	codeRoot := root
	if target != nil {
		var err error
		codeRoot, err = inspectNterchangeRemoteRoot(*target, root, compose, appService, media, database, app)
		if err != nil {
			return facts, err
		}
	}
	if !mount(app, "/var/www/site", codeRoot, false) || !mount(app, "/var/www/site/var", filepath.Join(root, "var"), true) {
		return facts, fmt.Errorf("app must bind this site's root read-only and its var directory writable")
	}
	for _, m := range media {
		location := filepath.Join(root, m)
		if m == "public_html/upload" && !mount(app, "/var/www/site/"+m, location, true) {
			return facts, fmt.Errorf("app must bind its own upload directory writable")
		}
		data, err := output("realpath", "-e", "--", location)
		if err != nil || strings.TrimSpace(string(data)) != location {
			return facts, fmt.Errorf("media %q is missing or uses symlinked path components", m)
		}
	}
	for _, m := range db.Mounts {
		if m.Destination == "/var/lib/mysql" && (m.Type == "bind" || m.Type == "volume") && m.RW {
			facts.Storage = m.Type
		}
	}
	if facts.Storage == "" {
		return facts, fmt.Errorf("database has no writable persistent data mount")
	}
	if target == nil {
		envFile := filepath.Join(root, ".env")
		for _, m := range app.Mounts {
			if m.Destination == "/var/www/site/.env" {
				if m.Type != "bind" || m.RW {
					return facts, fmt.Errorf("local app environment file must be a read-only bind")
				}
				envFile = m.Source
			}
		}
		env, err := config.ReadEnvFile(envFile)
		if err != nil {
			return facts, fmt.Errorf("cannot read local app environment file")
		}
		for _, assignment := range app.Config.Env {
			key, value, ok := strings.Cut(assignment, "=")
			if ok {
				env[key] = value
			}
		}
		dbEnv := map[string]string{}
		for _, assignment := range db.Config.Env {
			key, value, ok := strings.Cut(assignment, "=")
			if ok {
				dbEnv[key] = value
			}
		}
		facts.Database = dbEnv["MYSQL_DATABASE"]
		host, schema, user, password, publicURL := env["DB_SERVER"], env["DB_DATABASE"], env["DB_SERVER_USERNAME"], env["DB_SERVER_PASSWORD"], env["PUBLIC_URL"]
		// PetroChina reads these from the container environment; other sites load dotenv.
		if env["DB_NAME"] != "" {
			host, schema, user, password, publicURL = env["DB_HOST"], env["DB_NAME"], env["DB_USER"], env["DB_PASSWORD"], env["PUBLIC_SITE"]
		}
		if facts.Database == "" || schema != facts.Database || host != "db" || dbEnv["MYSQL_USER"] == "" || dbEnv["MYSQL_PASSWORD"] == "" || user != dbEnv["MYSQL_USER"] || password != dbEnv["MYSQL_PASSWORD"] {
			return facts, fmt.Errorf("local app database host/schema/credentials must match the Compose db service")
		}
		if err := project.ValidateName(facts.Database); err != nil {
			return facts, fmt.Errorf("unsafe local database name")
		}
		u, err := url.Parse(publicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || u.User != nil {
			return facts, fmt.Errorf("local public URL must be an HTTP(S) loopback URL")
		}
		// Inspect actual networks, not just the recipe, before allowing a data pull.
		for _, c := range containers {
			if len(c.NetworkSettings.Networks) == 0 {
				return facts, fmt.Errorf("local app and db must use internal Docker networks")
			}
			for _, network := range c.NetworkSettings.Networks {
				data, err := output("docker", "network", "inspect", network.NetworkID)
				var records []struct{ Internal bool }
				if err != nil || json.Unmarshal(data, &records) != nil || len(records) != 1 || !records[0].Internal {
					return facts, fmt.Errorf("local app and db must have no external network access")
				}
			}
		}
	} else {
		facts.Database = database
		for _, assignment := range app.Config.Env {
			key, value, _ := strings.Cut(assignment, "=")
			switch key {
			case "DB_HOST", "DB_SERVER":
				if value != "db" {
					return facts, fmt.Errorf("remote app database host override does not match db service")
				}
			case "DB_NAME", "DB_DATABASE":
				if value != database {
					return facts, fmt.Errorf("remote app database override does not match inventory")
				}
			}
		}
		client := false
		for _, m := range db.Mounts {
			if m.Destination == "/run/secrets/mysql-client.cnf" && m.Type == "bind" && !m.RW {
				client = true
			}
		}
		if !client {
			return facts, fmt.Errorf("remote db requires its read-only /run/secrets/mysql-client.cnf mount")
		}
		// Read only DB host/schema; dotenv contents and credentials stay on target.
		envFile := ""
		for _, m := range app.Mounts {
			if m.Destination == "/var/www/site/.env" && m.Type == "bind" && !m.RW {
				envFile = m.Source
			}
		}
		if envFile == "" {
			return facts, fmt.Errorf("remote app requires its own read-only environment file mount")
		}
		script := "import pathlib,sys; lines=pathlib.Path(sys.argv[1]).read_text().splitlines(); values={line.split('=',1)[0]:line.split('=',1)[1].strip().strip(chr(34)+chr(39)) for line in lines if line.startswith(('DB_DATABASE=','DB_SERVER='))}; print(values.get('DB_SERVER','')+'\\n'+values.get('DB_DATABASE',''))"
		data, err := output("python3", "-c", script, envFile)
		if err != nil || strings.TrimSpace(string(data)) != "db\n"+database {
			return facts, fmt.Errorf("remote app DB_SERVER/DB_DATABASE does not match the db service and inventory")
		}
	}
	query := "SELECT DEFAULT_CHARACTER_SET_NAME,DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='" + facts.Database + "'; SELECT COALESCE(ENGINE,'VIEW'),COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='" + facts.Database + "' GROUP BY ENGINE; SELECT @@GLOBAL.event_scheduler;"
	args := []string{"docker", "exec", db.ID}
	if target == nil {
		args = append(args, "sh", "-c", `export MYSQL_PWD="$MYSQL_PASSWORD"; exec mysql --user="$MYSQL_USER" --batch --skip-column-names --raw -e "$1"`, "sh", query)
	} else {
		args = append(args, "mysql", "--defaults-extra-file=/run/secrets/mysql-client.cnf", "--batch", "--skip-column-names", "--raw", "-e", query)
	}
	data, err := output(args...)
	if err != nil {
		return facts, fmt.Errorf("read database metadata: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 || len(strings.Split(lines[0], "\t")) != 2 {
		return facts, fmt.Errorf("database schema metadata is missing")
	}
	first := strings.Split(lines[0], "\t")
	facts.SQL.Charset, facts.SQL.Collation = first[0], first[1]
	facts.SQL.Engines = lines[1 : len(lines)-1]
	if target == nil && lines[len(lines)-1] != "OFF" && lines[len(lines)-1] != "DISABLED" {
		return facts, fmt.Errorf("local MySQL event_scheduler must be OFF before pulling data")
	}
	return facts, nil
}

func cmdNterchangePull(root string, metadata *projectMetadata, argv []string) int {
	check := false
	filtered := make([]string, 0, len(argv))
	for _, arg := range argv {
		if arg == "--check" {
			check = true
		} else {
			filtered = append(filtered, arg)
		}
	}
	remote, opts, ok := parseEnvRemoteSyncArgs("pull", filtered)
	if !ok {
		return 1
	}
	if (opts.execute && (opts.dryRun || check)) || (opts.yes && !opts.execute) || (opts.execute && opts.nonInteractive && !opts.yes) {
		fmt.Fprintln(os.Stderr, "Use --execute [--yes] to import, or --check/--dry-run alone. Non-interactive import requires --execute --yes.")
		return 1
	}
	if check && opts.dryRun {
		fmt.Fprintln(os.Stderr, "Choose --check (read-only connections) or --dry-run (no connections).")
		return 1
	}
	target, err := resolveNterchangePullTarget(metadata, remote)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	mode := "dry-run"
	if check {
		mode = "check"
	} else if opts.execute {
		mode = "execute"
	}
	printEnvRemoteSyncPreflight("pull", envConfig{ProjectSlug: metadata.Project.Slug, EnvDir: root}, envRemoteSyncTarget{
		RemoteName: remote, SiteID: target.Site.Name + "." + target.SSH.Name, Env: target.Site.Env,
		Provider: "ssh", TargetLabel: "target", TargetRef: target.SSH.Name, URL: target.Site.URL,
		SSHHost: target.SSH.Host, SSHUser: target.SSH.User, SSHPort: fmt.Sprint(target.SSH.Port),
	}, mode)
	fmt.Println("\nData:")
	printIndentedDetailRows([]detailRow{
		{label: "compose:", value: filepath.Join(root, "compose.yaml")},
		{label: "media:", value: strings.Join(metadata.Nterchange.MediaPaths(), ", ")},
		{label: "preserve:", value: "local settings, credentials, source, var, mail capture and integration restrictions"},
	}, 2)
	if !check && !opts.execute {
		fmt.Println("Plan only. No SSH, Docker, database or filesystem changes. Use --check to inspect source and destination without importing data.")
		return 0
	}
	if opts.execute && !opts.yes {
		confirmed, err := envRemoteSyncConfirm("Replace this local database and media with the selected remote? Creates a safety copy and pauses only the local app. MyISAM source exports briefly lock this site's tables for reading.", false)
		if err != nil || !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted; no connections or data changes.")
			return 1
		}
	}
	// Re-read inventory rather than executing a plan built only from disposable cache.
	records, err := discoverSSHTargetSites(target.SSH.record())
	if err != nil {
		fmt.Fprintln(os.Stderr, "Verify current remote inventory:", err)
		return 1
	}
	matched := false
	for _, record := range records {
		if firstRecordString(record, "env_id") == target.Site.Name+"."+target.SSH.Name+":"+target.Site.Env {
			nf, _ := record["nterchange"].(map[string]any)
			encoded, _ := json.Marshal(nf["media"])
			var media []string
			_ = json.Unmarshal(encoded, &media)
			matched = firstRecordString(record, "path") == target.Site.Path && firstRecordString(record, "url") == target.Site.URL && firstRecordString(nf, "compose_file") == target.Site.ComposeFile && firstRecordString(nf, "service") == target.Site.Service && firstRecordString(nf, "database") == target.Site.Database && reflect.DeepEqual(media, (&project.Nterchange{Media: target.Site.Media}).MediaPaths())
		}
	}
	if !matched {
		fmt.Fprintln(os.Stderr, "Remote inventory changed since discovery; run nf site refresh and review the plan again.")
		return 1
	}
	local, err := inspectNterchangeRuntime(root, filepath.Join(root, "compose.yaml"), "app", metadata.Nterchange.MediaPaths(), "", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Local preflight:", err)
		return 1
	}
	source, err := inspectNterchangeRuntime(target.Site.Path, target.Site.ComposeFile, target.Site.Service, target.Site.Media, target.Site.Database, &target.SSH)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Remote preflight:", err)
		return 1
	}
	fmt.Println("\nVerified source")
	printIndentedDetailRows([]detailRow{
		{label: "Schema", value: source.Database},
		{label: "Defaults", value: source.SQL.Charset + "/" + source.SQL.Collation},
		{label: "Storage", value: source.Storage},
		{label: "Engines", value: strings.Join(source.SQL.Engines, "; ")},
	}, 2)
	fmt.Println("\nVerified local")
	printIndentedDetailRows([]detailRow{
		{label: "Schema", value: local.Database},
		{label: "Defaults", value: local.SQL.Charset + "/" + local.SQL.Collation},
		{label: "Storage", value: local.Storage},
		{label: "Isolation", value: "internal networks, event scheduler off"},
	}, 2)
	if opts.execute {
		if err := executeNterchangePull(root, metadata.Project.Slug, target, local, source); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Println("Read-only preflight passed. No SQL rows or media were transferred, no snapshots created, and no containers or settings changed.")
	return 0
}
