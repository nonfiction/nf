package cli

// CLI help text for command groups.

import (
	"flag"
	"fmt"
)

const helpCommandMaxWidth = 36

func buildParser() *flag.FlagSet { return flag.NewFlagSet("nf", flag.ContinueOnError) }

type helpLine struct {
	Command     string
	Description string
}

type helpSection struct {
	Title string
	Lines []helpLine
}

func cliCommandAlias(name string) string {
	switch name {
	case "ls":
		return "list"
	case "rm":
		return "remove"
	case "sh":
		return "shell"
	default:
		return name
	}
}

func printGroupHelp(title string, lines []helpLine) {
	fmt.Println(title)
	fmt.Println("\nCommands:")
	printHelpLines(lines)
}

func printCommandHelp(title string, commands []helpLine, sections ...helpSection) {
	fmt.Println(title)
	fmt.Println("\nCommands:")
	printHelpLines(commands)
	for _, section := range sections {
		if len(section.Lines) == 0 {
			continue
		}
		fmt.Printf("\n%s:\n", section.Title)
		printHelpLines(section.Lines)
	}
}

func printHelpLines(lines []helpLine) {
	printHelpLinesWithIndent(lines, "  ", helpCommandMaxWidth)
}

func printHelpLinesWithIndent(lines []helpLine, indent string, maxWidth int) {
	width := 0
	for _, line := range lines {
		if line.Description == "" || len(line.Command) > maxWidth {
			continue
		}
		if len(line.Command) > width {
			width = len(line.Command)
		}
	}
	if width == 0 {
		width = maxWidth
	}
	for _, line := range lines {
		if line.Command == "" && line.Description == "" {
			fmt.Println()
			continue
		}
		if line.Description == "" {
			fmt.Printf("%s%s\n", indent, line.Command)
			continue
		}
		if len(line.Command) > maxWidth {
			fmt.Printf("%s%s\n", indent, line.Command)
			fmt.Printf("%s  %s\n", indent, line.Description)
			continue
		}
		fmt.Printf("%s%-*s  %s\n", indent, width, line.Command, line.Description)
	}
}

func runProviderHelp() int {
	printCommandHelp("provider", []helpLine{
		{"list, ls", "list provider integrations"},
		{"check [provider]", "run provider healthcheck"},
		{"show [provider]", "show cached provider metadata"},
	}, helpSection{"Options", []helpLine{
		{"--json", "print JSON output"},
	}})
	return 0
}

func runTargetHelp() int {
	printCommandHelp("target", []helpLine{
		{"list, ls", "list deployable targets"},
		{"show <target>", "show a deployable target"},
		{"password [target]", "show target password only"},
		{"refresh", "refresh targets from providers"},
		{},
		{"add linode <name>", "create a Linode target"},
		{"remove, rm <target>", "remove an empty Linode target"},
	}, helpSection{"Password Options", []helpLine{
		{"--root", "show the target root password"},
		{"--db", "show the target database UI password"},
	}}, helpSection{"Add Options", []helpLine{
		{"--region <region>", "Linode region"},
		{"--type <type>", "Linode instance type"},
		{"--image <image>", "Linode image"},
		{"--db-user <user>", "database UI/admin user"},
		{"--ssh-user <user>", "standard SSH user"},
	}}, helpSection{"Mutation Options", []helpLine{
		{"--dry-run", "show the mutation plan only"},
		{"--execute", "execute the mutation plan"},
		{"--yes", "confirm mutation execution"},
		{"--non-interactive", "fail instead of prompting"},
	}})
	return 0
}

func runRemoteHelp() int {
	printGroupHelp("remote", []helpLine{
		{"list, ls", "list repo remotes"},
		{"show <name>", "show a repo remote"},
		{},
		{"add [name] [env]", "add a repo remote"},
		{"remove, rm <name>", "remove a repo remote"},
	})
	return 0
}

func runSiteHelp() int {
	printCommandHelp("site", []helpLine{
		{"list, ls", "list sites or remote envs"},
		{"show [site|env]", "show a site or remote env"},
		{"refresh", "refresh local site cache"},
		{},
		{"shell, sh <env>", "shell into a remote env"},
		{"wp <env> -- <args>", "run wp-cli against a remote env"},
		{"password [site|env]", "show a site password only"},
		{},
		{"snapshot [env|list|remove|prune]", "manage remote snapshots"},
		{"export [env]", "create a full WordPress handoff export"},
		{"basicauth <action> [site|env]", "manage provider basic auth"},
		{},
		{"add <target> <site>", "create a live env"},
		{"staging <action> <site>", "manage staging env lifecycle"},
		{"remove, rm [site]", "remove a whole site"},
	}, helpSection{"Options", []helpLine{
		{"--envs", "include remote env records when listing"},
		{"--json", "print JSON output for show"},
		{"--wp", "show WordPress admin password"},
		{"--db", "show database password"},
		{"--basicauth", "show basic-auth password"},
		{"--output <path>", "site export destination"},
	}}, helpSection{"Add Options", []helpLine{
		{"--with-staging", "create live and staging envs together"},
		{"--kinsta-slug <slug>", "Kinsta site slug"},
		{"--region <region>", "provider region"},
		{"--php <version>", "Kinsta PHP version"},
	}}, helpSection{"Mutation Options", []helpLine{
		{"--dry-run", "show the mutation plan only"},
		{"--execute", "execute the mutation plan"},
		{"--yes", "confirm mutation execution"},
		{"--non-interactive", "fail instead of prompting"},
	}})
	return 0
}

func runDomainHelp() int {
	return runSiteDomainHelp()
}

func runConfigHelp() int {
	printCommandHelp("config", []helpLine{
		{"show", "show global config"},
		{"init", "initialize local config"},
		{},
		{"set-base-domain <domain>", "set provider base domain"},
		{"set-default-wp-email <email>", "set default WordPress email"},
		{"set-default-wp-user <user>", "set default WordPress user"},
		{"set-basicauth-default-user <user>", "set default basic-auth user"},
		{"set-db-default-user <user>", "set default database access user"},
	}, helpSection{"Docker Defaults", []helpLine{
		{"set-docker-db-image <image>", "set default Docker database image"},
		{"set-docker-wordpress-image <image>", "set default Docker WordPress image"},
		{"set-docker-user <user>", "set default Docker shell user"},
	}}, helpSection{"Kinsta Defaults", []helpLine{
		{"set-kinsta-default-region <region>", "set default Kinsta region"},
		{"set-kinsta-default-php <version>", "set default Kinsta PHP version"},
	}}, helpSection{"Linode Defaults", []helpLine{
		{"set-linode-default-region <region>", "set default Linode region"},
		{"set-linode-default-type <type>", "set default Linode type"},
		{"set-linode-default-image <image>", "set default Linode image"},
		{"set-linode-default-user <user>", "set default Linode SSH user"},
	}})
	return 0
}

func runPasswordHelp() int {
	printCommandHelp("password", []helpLine{
		{"derive <scope> <value...>", "derive a password"},
		{},
		{"show-salt", "show the masked password salt"},
		{"set-salt <salt>", "save the shared password salt"},
	}, helpSection{"Options", []helpLine{
		{"--password-version <N>", "derive with a project password version"},
	}})
	return 0
}

func runCompletionHelp() int {
	printGroupHelp("completion", []helpLine{
		{"bash", "print bash completion script"},
		{"zsh", "print zsh completion script"},
	})
	return 0
}
