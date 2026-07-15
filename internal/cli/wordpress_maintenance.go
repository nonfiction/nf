package cli

import (
	"fmt"
	"strings"
)

const rewriteRulesRegeneratedMessage = "Rewrite rules regenerated."

func wpRewriteFlushArgs() []string {
	return []string{"rewrite", "flush"}
}

func envWpRewriteFlushArgs(cfg envConfig) []string {
	return envWpArgs(cfg, wpRewriteFlushArgs()...)
}

func wpRewriteFlushCommand(wpCommand string) string {
	return strings.TrimSpace(wpCommand) + " " + renderCommandArgs(wpRewriteFlushArgs())
}

func remoteWPCommand(target envRemoteSyncTarget, args ...string) string {
	command := strings.TrimSpace(target.WPCommand) + " --path=" + shellQuoteArg(target.WordPressPath)
	if len(args) == 0 {
		return command
	}
	return command + " " + renderCommandArgs(args)
}

func remoteWPSSHArgs(target envRemoteSyncTarget, args ...string) []string {
	return remoteSSHArgs(target, remoteWPCommand(target, args...))
}

func rewriteFlushShellStep(command, failureMessage string) string {
	return fmt.Sprintf("if %s; then printf '%%s\\n' %s; else printf '%%s\\n' %s >&2; exit 1; fi", command, shellQuoteArg(rewriteRulesRegeneratedMessage), shellQuoteArg(failureMessage))
}

func flushLocalRewriteRules(cfg envConfig) error {
	if err := runCommandSpec(execSpec{Dir: localEnvDir(cfg), Args: envWpRewriteFlushArgs(cfg)}); err != nil {
		return fmt.Errorf("failed to flush WordPress rewrite rules in the local environment: %w", err)
	}
	fmt.Println(rewriteRulesRegeneratedMessage)
	return nil
}

func flushRemoteRewriteRules(target envRemoteSyncTarget) error {
	args := remoteWPSSHArgs(target, wpRewriteFlushArgs()...)
	if err := runSSHCommandFn(args); err != nil {
		return fmt.Errorf("failed to flush WordPress rewrite rules on %s: %w", target.Env, err)
	}
	fmt.Println(rewriteRulesRegeneratedMessage)
	return nil
}
