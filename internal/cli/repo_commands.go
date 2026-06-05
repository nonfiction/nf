package cli

// Repository command runners execute commands defined in nf.json.
//
// String tasks run through the shell for human-authored shortcuts. Array tasks
// run directly so callers can avoid shell parsing when they need exact argv.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type repoCommandRunner interface {
	Execute(root string, extraArgs []string) error
	Render() string
}

type shellCommandRunner string

func (c shellCommandRunner) Execute(root string, extraArgs []string) error {
	printShellCommand(string(c), extraArgs)
	cmd := exec.Command("sh", append([]string{"-lc", string(c), "sh"}, extraArgs...)...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (c shellCommandRunner) Render() string { return string(c) }

type argvCommandRunner []string

func (c argvCommandRunner) Execute(root string, extraArgs []string) error {
	if len(c) == 0 {
		return fmt.Errorf("unsupported repo command type")
	}
	printCommandArgs(append(append([]string{}, c...), extraArgs...))
	cmd := exec.Command(c[0], append(c[1:], extraArgs...)...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (c argvCommandRunner) Render() string { return strings.Join(c, " ") }
