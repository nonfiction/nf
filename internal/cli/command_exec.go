package cli

// Command execution helpers with command previews.
//
// Theme, env, SSH, rsync, and snapshot flows share these helpers so every
// executed command is rendered consistently before it runs.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type execSpec struct {
	Dir  string
	Args []string
}

func runCommandSpec(spec execSpec) error {
	if len(spec.Args) == 0 {
		return fmt.Errorf("unsupported repo command type")
	}
	printCommandArgs(spec.Args)
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runCommandSpecQuiet(spec execSpec) error {
	if len(spec.Args) == 0 {
		return fmt.Errorf("unsupported repo command type")
	}
	printCommandArgs(spec.Args)
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runRsyncCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("unsupported rsync command")
	}
	printCommandArgs(args)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func printShellCommand(command string, extraArgs []string) {
	if len(extraArgs) == 0 {
		fmt.Printf("> %s\n", command)
		return
	}
	fmt.Printf("> %s -- %s\n", command, renderCommandArgs(extraArgs))
}

func printCommandArgs(args []string) {
	fmt.Printf("> %s\n", renderCommandArgs(args))
}

func renderCommandArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuoteArg(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	if !strings.ContainsAny(arg, " \t\n\r'\"$`\\!&|;<>(){}[]*?~") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}
