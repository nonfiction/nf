package cli

// Package-level seams used by commands and tests.
//
// These variables keep command code small while allowing tests to replace
// network, SSH, provider, and prompt behavior without global monkey patches.

import (
	"net"

	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/target/provision"
	"github.com/nonfiction/nf/internal/ui"
)

type ProjectError struct{ Msg string }

func (e ProjectError) Error() string { return e.Msg }

var (
	runLinodeDeleteFn                     = runLinodeDelete
	deleteDNSRecordFn                     = provision.DeleteDNSimpleARecord
	deleteDNSTXTRecordFn                  = provision.DeleteDNSimpleTXTRecord
	deleteDNSTypedRecordFn                = provision.DeleteDNSimpleRecord
	listDNSTypedRecordsFn                 = provision.ListDNSimpleRecords
	upsertDNSRecordFn                     = provision.UpsertDNSimpleRecord
	providerCheckDNSimpleFn               = checkDNSimpleProvider
	providerCheckKinstaFn                 = checkKinstaProvider
	providerCheckLinodeFn                 = checkLinodeProvider
	kinstaProvisionSiteFn                 = provisionKinstaSite
	kinstaProvisionStagingFn              = provisionKinstaStaging
	kinstaRemoveSiteFn                    = removeKinstaSite
	kinstaPrepareDomainFn                 = prepareKinstaSiteDomain
	kinstaPrimaryDomainFn                 = primaryKinstaSiteDomain
	kinstaRemoveDomainFn                  = removeKinstaSiteDomain
	siteDomainLookupHostFn                = net.LookupHost
	siteDomainLookupTXTFn                 = net.LookupTXT
	siteDomainLookupCNAMEFn               = net.LookupCNAME
	siteDomainHTTPStatusFn                = checkSiteDomainHTTP
	siteDomainHTTPSStatusFn               = checkSiteDomainHTTPS
	siteDomainTLSStatusFn                 = checkSiteDomainTLS
	siteDomainOriginTLSFn                 = checkSiteDomainOriginTLS
	siteDomainCloudflareIPRangesFn        = loadCloudflareIPRanges
	targetSSHReachableFn                  = targetSSHReachable
	runSSHScriptFn                        = runSSHScript
	runSSHCommandFn                       = runSSHCommand
	runSSHStdinCommandFn                  = runSSHStdinCommand
	runSSHStdinOutputFn                   = runSSHStdinOutput
	runSSHOutputFn                        = runSSHOutput
	runRsyncCommandFn                     = runRsyncCommand
	runCommandSpecOutputSilentFn          = runCommandSpecOutputSilent
	localAvailableDiskBytesFn             = localAvailableDiskBytes
	localWordPressTransferEstimateBytesFn = localWordPressTransferEstimateBytes
	localPushTransferEstimateBytesFn      = localPushTransferEstimateBytes
	localSnapshotExpandedSizeBytesFn      = localSnapshotExpandedSizeBytes
	localPushTransferArchiveSizeBytesFn   = localPushTransferArchiveSizeBytes
	localPushTransferExpandedSizeBytesFn  = localPushTransferExpandedSizeBytes
	targetSelectFn                        = ui.Select
	providerSelectFn                      = ui.Select
	siteAddSelectFn                       = ui.Select
	siteAddPromptStringFn                 = ui.PromptString
	siteAddConfirmFn                      = ui.Confirm
	siteSelectFn                          = ui.Select
	siteDomainSelectFn                    = ui.Select
	siteDomainMultiSelectFn               = ui.MultiSelect
	siteDomainMultiSelectNoneFn           = ui.MultiSelectNoneSelected
	siteDomainPromptStringFn              = ui.PromptString
	remoteSelectFn                        = ui.Select
	remotePromptString                    = ui.PromptString
	passwordDeriveSelectFn                = ui.Select
	passwordDerivePromptString            = ui.PromptString
	siteIsInteractiveFn                   = envwizard.IsInteractiveTerminal
)
