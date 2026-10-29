package cli

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func lockNterchangeMyISAM(root string, facts nterchangeRuntimeFacts, target *sshTarget) (func() error, error) {
	needed := false
	for _, engine := range facts.SQL.Engines {
		needed = needed || strings.HasPrefix(engine, "MyISAM\t")
	}
	if !needed {
		return func() error { return nil }, nil
	}
	args := append(nterchangeSQLArgs(facts, false, target != nil), "--unbuffered")
	query := "SELECT HEX(TABLE_NAME) FROM information_schema.TABLES WHERE TABLE_SCHEMA='" + facts.Database + "' ORDER BY TABLE_NAME;"
	var data []byte
	var err error
	if target == nil {
		data, err = nterchangeOutputFn(root, append(args, "-e", query))
	} else {
		data, err = nterchangeRemoteOutput(*target, append(args, "-e", query)...)
		args = nterchangeRemoteCommand(*target, args)
	}
	if err != nil {
		return nil, err
	}
	var tables []string
	for _, line := range strings.Fields(string(data)) {
		name, err := hex.DecodeString(line)
		if err != nil || len(name) == 0 {
			return nil, fmt.Errorf("invalid MyISAM schema table inventory")
		}
		tables = append(tables, "`"+facts.Database+"`.`"+strings.ReplaceAll(string(name), "`", "``")+"` READ")
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("MyISAM tables disappeared before export")
	}
	fmt.Println("Consistent mixed-engine export: hold READ locks on this schema's tables until dump completes. Other schemas remain unlocked.")
	printCommandArgs(args)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir, cmd.Stderr = root, os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		input.Close()
		return nil, err
	}
	lines := bufio.NewScanner(output)
	readMarker := func(marker string) error {
		ready := make(chan bool, 1)
		go func() { ready <- lines.Scan() && lines.Text() == marker }()
		select {
		case ok := <-ready:
			if ok {
				return nil
			}
		case <-time.After(60 * time.Second):
			_ = cmd.Process.Kill()
			<-ready
		}
		return fmt.Errorf("MyISAM read-lock session did not confirm %s", marker)
	}
	_, err = fmt.Fprintf(input, "SET SESSION lock_wait_timeout=30; SET autocommit=0; LOCK TABLES %s; SELECT 'nf_locked';\n", strings.Join(tables, ","))
	if err == nil {
		err = readMarker("nf_locked")
	}
	if err != nil {
		input.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return func() error {
		_, err := fmt.Fprintln(input, "UNLOCK TABLES; SELECT 'nf_unlocked';")
		if err == nil {
			err = readMarker("nf_unlocked")
		}
		input.Close()
		waitErr := cmd.Wait()
		if err != nil {
			return err
		}
		return waitErr
	}, nil
}
