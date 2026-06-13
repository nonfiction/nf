package cli

// Package-level seams used by commands and tests.
//
// These variables keep command code small while allowing tests to replace
// network, SSH, provider, and prompt behavior without global monkey patches.

import (
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/target/provision"
	"github.com/nonfiction/nf/internal/ui"
)

type ProjectError struct{ Msg string }

func (e ProjectError) Error() string { return e.Msg }

var (
	runLinodeDeleteFn        = runLinodeDelete
	deleteDNSRecordFn        = provision.DeleteDNSimpleARecord
	deleteDNSTXTRecordFn     = provision.DeleteDNSimpleTXTRecord
	deleteDNSTypedRecordFn   = provision.DeleteDNSimpleRecord
	upsertDNSRecordFn        = provision.UpsertDNSimpleRecord
	providerCheckDNSimpleFn  = checkDNSimpleProvider
	providerCheckKinstaFn    = checkKinstaProvider
	providerCheckLinodeFn    = checkLinodeProvider
	kinstaProvisionSiteFn    = provisionKinstaSite
	kinstaProvisionStagingFn = provisionKinstaStaging
	kinstaRemoveSiteFn       = removeKinstaSite
	kinstaPrepareDomainFn    = prepareKinstaSiteDomain
	kinstaPrimaryDomainFn    = primaryKinstaSiteDomain
	targetSSHReachableFn     = targetSSHReachable
	runSSHScriptFn           = runSSHScript
	runSSHCommandFn          = runSSHCommand
	runSSHStdinCommandFn     = runSSHStdinCommand
	runSSHOutputFn           = runSSHOutput
	runRsyncCommandFn        = runRsyncCommand
	targetSelectFn           = ui.Select
	providerSelectFn         = ui.Select
	siteSelectFn             = ui.Select
	remoteSelectFn           = ui.Select
	remotePromptString       = ui.PromptString
	siteIsInteractiveFn      = envwizard.IsInteractiveTerminal
)
