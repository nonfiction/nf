package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nonfiction/nf/internal/config"
)

var nterchangeStreamFn = func(root string, args []string, input io.Reader, output io.Writer) error {
	printCommandArgs(args)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = root, input, output, os.Stderr
	return cmd.Run()
}

func nterchangeSQLArgs(facts nterchangeRuntimeFacts, dump, remote bool) []string {
	tool := "mysql"
	if dump {
		tool = "mysqldump"
	}
	args := []string{"docker", "exec"}
	if !dump {
		args = append(args, "-i")
	}
	args = append(args, facts.DBID)
	client := []string{tool}
	if remote {
		client = append(client, "--defaults-extra-file=/run/secrets/mysql-client.cnf")
	} else {
		args = append(args, "sh", "-c", `export MYSQL_PWD="$MYSQL_PASSWORD"; exec "$@" --user="$MYSQL_USER"`, "sh")
	}
	if dump {
		client = append(client, "--set-gtid-purged=OFF", "--no-tablespaces", "--column-statistics=0", "--hex-blob", "--default-character-set=utf8mb4", "--skip-add-locks", "--skip-dump-date", "--skip-triggers", "--routines=false", "--events=false")
		// Mixed MyISAM/InnoDB exports use explicit READ locks in a separate
		// connection. mysqldump's READ LOCAL locks still allow MyISAM inserts.
		myisam := false
		for _, engine := range facts.SQL.Engines {
			myisam = myisam || strings.HasPrefix(engine, "MyISAM\t")
		}
		if myisam {
			client = append(client, "--skip-lock-tables")
		} else {
			client = append(client, "--single-transaction", "--skip-lock-tables")
		}
	} else {
		client = append(client, "--binary-mode", "--local-infile=0", "--batch", "--skip-column-names")
	}
	return append(args, append(client, facts.Database)...)
}

func nterchangeRemoteCommand(target sshTarget, args []string) []string {
	return append(sshTargetArgs(target), renderCommandArgs(append([]string{"sudo", "-n"}, args...)))
}

// Reject stored SQL objects until their definer and execution policy is supported.
// Silently omitting them from a development copy would misrepresent the source.
func checkNterchangeTransferSQL(root string, facts nterchangeRuntimeFacts, target *sshTarget) error {
	for _, engine := range facts.SQL.Engines {
		if !strings.HasPrefix(engine, "InnoDB\t") && !strings.HasPrefix(engine, "MyISAM\t") {
			return fmt.Errorf("pull supports only InnoDB and MyISAM base tables; found %q", engine)
		}
	}
	schema := facts.Database
	query := "SELECT (SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='" + schema + "')+(SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='" + schema + "')+(SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA='" + schema + "');"
	args := append(nterchangeSQLArgs(facts, false, target != nil), "-e", query)
	var data []byte
	var err error
	if target != nil {
		data, err = nterchangeRemoteOutput(*target, args...)
	} else {
		data, err = nterchangeOutputFn(root, args)
	}
	if err != nil || strings.TrimSpace(string(data)) != "0" {
		return fmt.Errorf("pull requires no stored routines, triggers or events in source/destination")
	}
	return nil
}

func checkNterchangeMedia(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("media must contain only regular files/directories: %s", path)
		}
		return nil
	})
}

func nterchangeCopyMedia(root, from, to string, restore bool) error {
	if err := checkNterchangeMedia(from); err != nil {
		return err
	}
	if err := os.MkdirAll(to, 0700); err != nil {
		return err
	}
	if err := checkNterchangeMedia(to); err != nil {
		return err
	}
	args := []string{"rsync", "-a", "--no-owner", "--no-group", "--delete", "--no-links", "--no-devices", "--no-specials"}
	if !restore {
		args = append(args, "--chmod=Du=rwx,Dgo=rx,Fu=rw,Fgo=r")
	}
	args = append(args, "--", from+"/", to+"/")
	return nterchangeStreamFn(root, args, nil, os.Stdout)
}

func nterchangeDump(root, file string, facts nterchangeRuntimeFacts, target *sshTarget) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	args := nterchangeSQLArgs(facts, true, target != nil)
	if target != nil {
		args = nterchangeRemoteCommand(*target, args)
	}
	release, err := lockNterchangeMyISAM(root, facts, target)
	if err != nil {
		f.Close()
		return err
	}
	err = nterchangeStreamFn(root, args, nil, f)
	lockErr := release()
	closeErr := f.Close()
	if lockErr != nil {
		return fmt.Errorf("source read-lock connection failed: %w", lockErr)
	}
	if err != nil {
		return err
	}
	return closeErr
}

func nterchangeImport(root, file string, facts nterchangeRuntimeFacts, defaults nterchangeDatabaseFacts) error {
	// Names come from validated runtime metadata, never raw user SQL.
	valid := regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	if !valid.MatchString(defaults.Charset) || !valid.MatchString(defaults.Collation) {
		return fmt.Errorf("unsafe database charset/collation")
	}
	query := "SELECT CONCAT('DROP TABLE `',REPLACE(TABLE_NAME,'`','``'),'`;') FROM information_schema.TABLES WHERE TABLE_SCHEMA='" + facts.Database + "';"
	data, err := nterchangeOutputFn(root, append(nterchangeSQLArgs(facts, false, false), "-e", query))
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	prefix := "SET FOREIGN_KEY_CHECKS=0;\n" + string(data) + "\nSET FOREIGN_KEY_CHECKS=1;\nALTER DATABASE `" + facts.Database + "` CHARACTER SET " + defaults.Charset + " COLLATE " + defaults.Collation + ";\n"
	return nterchangeStreamFn(root, nterchangeSQLArgs(facts, false, false), io.MultiReader(strings.NewReader(prefix), f), io.Discard)
}

func executeNterchangePull(root, slug string, target nterchangePullTarget, local, source nterchangeRuntimeFacts) (err error) {
	if local.DBID == source.DBID {
		return fmt.Errorf("source and destination database containers must differ")
	}
	// Import never runs as root or an account with privileges outside this schema.
	grants, err := nterchangeOutputFn(root, append(nterchangeSQLArgs(local, false, false), "-e", "SHOW GRANTS;"))
	if err != nil {
		return err
	}
	allowedGrant := regexp.MustCompile("^GRANT [A-Z_, ]+ ON " + regexp.QuoteMeta("`"+local.Database+"`.*") + " TO ")
	for _, grant := range strings.Split(strings.TrimSpace(string(grants)), "\n") {
		if !strings.HasPrefix(grant, "GRANT USAGE ON *.* TO ") && !allowedGrant.MatchString(grant) {
			return fmt.Errorf("local import user must have privileges only on its own schema")
		}
	}
	base := filepath.Join(config.DataHome(), "nterchange", "pulls", slug)
	if err := os.MkdirAll(base, 0700); err != nil {
		return err
	}
	lock := filepath.Join(base, "pull.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("another pull or interrupted pull owns %s; inspect before removing it: %w", lock, err)
	}
	fmt.Fprintf(f, "pid=%d\nroot=%s\n", os.Getpid(), root)
	f.Close()
	defer os.Remove(lock)
	work, err := os.MkdirTemp(base, "pull-")
	if err != nil {
		return err
	}
	before, stage := filepath.Join(work, "before"), filepath.Join(work, "source")
	for _, dir := range []string{before, stage} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
	}
	fmt.Printf("Pull recovery directory: %s\n", work)
	defer func() {
		if err == nil {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := checkNterchangeTransferSQL(root, local, nil); err != nil {
		return err
	}
	if err := checkNterchangeTransferSQL(root, source, &target.SSH); err != nil {
		return err
	}
	if err := nterchangeDump(root, filepath.Join(stage, "database.sql"), source, &target.SSH); err != nil {
		return fmt.Errorf("source export failed before local writes: %w", err)
	}
	for _, media := range target.Site.Media {
		from, to := filepath.Join(root, media), filepath.Join(stage, media)
		// Seed locally so rsync transfers changed bytes rather than every upload.
		if err := nterchangeCopyMedia(root, from, to, true); err != nil {
			return err
		}
		args := []string{"rsync", "-rlt", "--checksum", "--delete", "--no-links", "--no-devices", "--no-specials", "--chmod=Du=rwx,Dgo=,Fu=rw,Fgo=", "--protect-args", "--rsync-path=sudo -n rsync", "-e", renderCommandArgs(sshTargetArgs(target.SSH)[:len(sshTargetArgs(target.SSH))-2]), "--", target.SSH.User + "@" + target.SSH.Host + ":" + filepath.Join(target.Site.Path, media) + "/", to + "/"}
		// Refuse source links/special files instead of silently skipping them.
		data, err := nterchangeRemoteOutput(target.SSH, "find", filepath.Join(target.Site.Path, media), "!", "-type", "f", "!", "-type", "d", "-print", "-quit")
		if err != nil || len(data) != 0 {
			return fmt.Errorf("source media has unsupported entries or cannot be inspected")
		}
		if err := nterchangeStreamFn(root, args, nil, os.Stdout); err != nil {
			return err
		}
		if err := checkNterchangeMedia(to); err != nil {
			return err
		}
	}
	// Recheck actual destination before freezing it, after potentially long downloads.
	current, err := inspectNterchangeRuntime(root, filepath.Join(root, "compose.yaml"), "app", target.Site.Media, "", nil)
	if err != nil || current.AppID != local.AppID || current.DBID != local.DBID || current.Database != local.Database {
		return fmt.Errorf("destination changed during download; local data was not changed")
	}
	if _, err := nterchangeOutputFn(root, []string{"docker", "pause", local.AppID}); err != nil {
		return err
	}
	resume := true
	defer func() {
		if resume {
			if _, e := nterchangeOutputFn(root, []string{"docker", "unpause", local.AppID}); e != nil {
				err = fmt.Errorf("%v; app resume failed: %w", err, e)
			}
		}
	}()
	if err := nterchangeDump(root, filepath.Join(before, "database.sql"), local, nil); err != nil {
		return err
	}
	for _, media := range target.Site.Media {
		if err := nterchangeCopyMedia(root, filepath.Join(root, media), filepath.Join(before, media), true); err != nil {
			return err
		}
	}
	manifest, err := json.MarshalIndent(struct {
		Root   string
		Source sshSiteEnv
		Local  nterchangeRuntimeFacts
	}{root, target.Site, local}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(before, "snapshot.json"), manifest, 0600); err != nil {
		return err
	}
	// snapshot.json marks a complete safety copy before the first destination write.
	apply := func(dir string, defaults nterchangeDatabaseFacts, restore bool) error {
		if err := nterchangeImport(root, filepath.Join(dir, "database.sql"), local, defaults); err != nil {
			return err
		}
		for _, media := range target.Site.Media {
			if err := nterchangeCopyMedia(root, filepath.Join(dir, media), filepath.Join(root, media), restore); err != nil {
				return err
			}
		}
		return nil
	}
	if err := apply(stage, source.SQL, false); err != nil {
		if recovery := apply(before, local.SQL, true); recovery != nil {
			resume = false
			return fmt.Errorf("pull failed: %v; recovery failed: %v; app LEFT PAUSED; retain %s", err, recovery, work)
		}
		return fmt.Errorf("pull failed; previous local SQL/media restored: %w", err)
	}
	fmt.Printf("Pulled SQL/media. Local settings/source/var unchanged. Safety copy: %s\n", before)
	return nil
}
