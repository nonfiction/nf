package cli

// CLI help text for command groups.

import (
	"flag"
	"fmt"
)

func buildParser() *flag.FlagSet { return flag.NewFlagSet("nf", flag.ContinueOnError) }

type helpLine struct {
	Command     string
	Description string
}

func cliCommandAlias(name string) string {
	switch name {
	case "ls":
		return "list"
	case "rm":
		return "remove"
	case "ssh":
		return "shell"
	default:
		return name
	}
}

func printGroupHelp(title string, lines []helpLine) {
	fmt.Println(title)
	fmt.Println("\nCommands:")
	width := 0
	for _, line := range lines {
		if len(line.Command) > width {
			width = len(line.Command)
		}
	}
	for _, line := range lines {
		if line.Command == "" && line.Description == "" {
			fmt.Println()
			continue
		}
		if line.Description == "" {
			fmt.Printf("  %s\n", line.Command)
			continue
		}
		fmt.Printf("  %-*s  %s\n", width, line.Command, line.Description)
	}
}

func runServerHelp() int {
	printGroupHelp("server", []helpLine{
		{"provision [flags]", "provision an infrastructure host"},
		{"list, ls", "list servers"},
		{"show <id-or-name>", "show a server"},
		{"root-password <id-or-name>", "derive the Linode root password"},
		{"delete <id-or-name> [flags]", "delete a server"},
	})
	return 0
}

func runProviderHelp() int {
	printGroupHelp("provider", []helpLine{
		{"list, ls", "list provider integrations"},
		{"check [provider] [--json]", "run provider healthcheck"},
		{"show [provider] [--json]", "show cached provider metadata"},
	})
	return 0
}

func runTargetHelp() int {
	printGroupHelp("target", []helpLine{
		{"list, ls", "list deployable targets"},
		{"show <target>", "show a deployable target"},
		{"refresh", "refresh targets from providers"},
		{"add linode <name> [flags]", "create a Linode target"},
		{"remove, rm <target>", "remove an empty Linode target"},
	})
	return 0
}

func runRemoteHelp() int {
	printGroupHelp("remote", []helpLine{
		{"list, ls", "list repo remotes"},
		{"show <name>", "show a repo remote"},
		{"add [name] [env]", "add a repo remote"},
		{"remove, rm <name>", "remove a repo remote"},
	})
	return 0
}

func runSiteHelp() int {
	printGroupHelp("site", []helpLine{
		{"list, ls [--envs]", "list sites or remote envs"},
		{"show [site|env] [--json]", "show a site or remote env"},
		{"shell, ssh <env>", "shell into a remote env"},
		{"wp <env> -- <args>", "run wp-cli against a remote env"},
		{"snapshot [env|list|remove|prune] [flags]", "manage remote snapshots"},
		{"password [site]", "show admin password only"},
		{"refresh", "refresh local site cache"},
		{"add <target> <site> [flags]", "create live and staging envs"},
		{"remove, rm [site] [flags]", "remove a site and both envs"},
	})
	return 0
}

func runConfigHelp() int {
	printGroupHelp("config", []helpLine{
		{"init", "initialize local config"},
		{"show", "show global config"},
		{"set-base-domain <domain>", "set provider base domain"},
		{"set-default-wp-email <email>", "set default WordPress email"},
		{"set-default-wp-user <user>", "set default WordPress user"},
		{"set-kinsta-default-region <region>", "set default Kinsta region"},
		{"set-kinsta-default-php <version>", "set default Kinsta PHP version"},
		{"set-linode-default-region <region>", "set default Linode region"},
		{"set-linode-default-type <type>", "set default Linode type"},
		{"set-linode-default-image <image>", "set default Linode image"},
		{"set-linode-default-user <user>", "set default Linode SSH user"},
	})
	return 0
}

func runPasswordHelp() int {
	printGroupHelp("password", []helpLine{
		{"show-salt", "show the masked password salt"},
		{"set-salt <salt>", "save the shared password salt"},
		{"derive <scope> [args...]", "derive a password"},
	})
	return 0
}

func runCompletionHelp() int {
	printGroupHelp("completion", []helpLine{
		{"bash", "print bash completion script"},
		{"zsh", "print zsh completion script"},
	})
	return 0
}
