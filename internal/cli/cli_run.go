package cli

// Root command router and topic-help dispatch.
//
// Keep this file small: it should decide where a command goes, not implement the
// command itself.

import (
	"fmt"
	"os"
)

func Run(argv []string) int {
	if len(argv) == 0 {
		return runHelp()
	}
	switch argv[0] {
	case "--help", "-h", "help":
		if len(argv) == 1 {
			return runHelp()
		}
		return runTopicHelp(argv[1:])
	case "__complete":
		return runComplete(argv[1:])
	case "completion":
		return runCompletion(argv[1:])
	case "version", "--version", "-v":
		return runVersion(argv[1:])
	case "provider":
		return runProvider(argv[1:])
	case "target":
		return runTarget(argv[1:])
	case "site":
		return runSite(argv[1:])
	case "refresh":
		return runRefresh(argv[1:])
	case "domain":
		return runDomain(argv[1:])
	case "remote":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runRemote(argv[1:])
	case "plugin":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runPlugin(argv[1:])
	case "env":
		if !envUpCommand(argv[1:]) && !envImportHelpCommand(argv[1:]) && rejectOutsideProject(argv[0]) {
			return 1
		}
		return runEnv(argv[1:])
	case "init":
		return runInit(argv[1:])
	case "theme":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runTheme(argv[1:])
	case "alias":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runAlias(argv[1:])
	case "define":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runDefine(argv[1:])
	case "config":
		return runConfig(argv[1:])
	case "password":
		return runPassword(argv[1:])
	default:
		fmt.Fprintf(os.Stderr, "unsupported command: %s\n", argv[0])
		return 1
	}
}

func envUpCommand(argv []string) bool {
	return len(argv) > 0 && argv[0] == "up"
}

func envImportHelpCommand(argv []string) bool {
	if len(argv) == 1 && argv[0] == "import" {
		return true
	}
	return len(argv) == 2 && argv[0] == "import" && (argv[1] == "help" || argv[1] == "--help" || argv[1] == "-h")
}

func runTopicHelp(argv []string) int {
	if len(argv) == 0 {
		return runHelp()
	}
	switch argv[0] {
	case "provider":
		return runProviderHelp()
	case "target":
		return runTargetHelp()
	case "site":
		if len(argv) > 1 {
			switch argv[1] {
			case "snapshot":
				return runSiteSnapshotHelp()
			case "staging":
				return runSiteStagingHelp()
			case "basicauth":
				return runSiteBasicAuthHelp()
			}
		}
		return runSiteHelp()
	case "refresh":
		return runRefreshHelp()
	case "domain":
		return runDomainHelp()
	case "remote":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runRemoteHelp()
	case "plugin":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		if len(argv) > 1 && argv[1] == "cache" {
			return runPluginCacheHelp()
		}
		return runPluginHelp()
	case "env":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		if len(argv) > 1 {
			switch argv[1] {
			case "snapshot":
				return runEnvSnapshot([]string{"help"})
			case "import":
				return runEnvImportHelp()
			}
		}
		return runEnvHelp()
	case "init":
		return runInitHelp()
	case "theme":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		if len(argv) > 1 && argv[1] == "cache" {
			return runThemeCacheHelp()
		}
		return runThemeHelp()
	case "alias":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runAliasHelp()
	case "define":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runDefineHelp()
	case "config":
		return runConfigHelp()
	case "password":
		return runPasswordHelp()
	case "completion":
		return runCompletionHelp()
	case "version":
		return runVersionHelp()
	default:
		return runHelp()
	}
}
