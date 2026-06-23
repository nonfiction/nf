package cli

// Interactive argument collection for `nf site add`.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

func resolveSiteAddArgs(args siteAddArgs, positionals []string) (siteAddArgs, error) {
	if len(positionals) > 2 {
		return args, ProjectError{Msg: "site add takes exactly target and site"}
	}
	if len(positionals) == 2 {
		args.target = positionals[0]
		args.site = positionals[1]
		return args, nil
	}
	if args.nonInteractive {
		return args, ProjectError{Msg: "site add requires target and site in non-interactive mode"}
	}
	if !siteIsInteractiveFn() {
		return args, ProjectError{Msg: "site add requires target and site"}
	}

	if len(positionals) == 1 {
		args.target = positionals[0]
	} else {
		target, err := promptSiteAddTarget()
		if err != nil {
			return args, err
		}
		args.target = target
	}

	selectedSite, err := promptSiteAddSite(args.target)
	if err != nil {
		return args, err
	}
	args.site = selectedSite

	provider, err := siteAddTargetProvider(args.target)
	if err != nil {
		return args, err
	}
	if provider == "kinsta" && strings.TrimSpace(args.kinstaSlug) == "" {
		kinstaSlug, err := promptSiteAddKinstaSlugIfNeeded(args)
		if err != nil {
			return args, err
		}
		args.kinstaSlug = kinstaSlug
	}
	if !args.withStagingSet {
		withStaging, err := promptSiteAddWithStaging()
		if err != nil {
			return args, err
		}
		args.withStaging = withStaging
		args.withStagingSet = true
	}
	return args, nil
}

func promptSiteAddTarget() (string, error) {
	targets, err := cachedTargets()
	if err != nil {
		return "", err
	}
	if len(targets) == 0 {
		return "", ProjectError{Msg: "No targets found."}
	}
	options := make([]ui.SelectOption, 0, len(targets))
	for _, target := range targets {
		value := siteAddTargetPickerValue(target)
		if value == "" {
			continue
		}
		label := value
		provider := strings.TrimSpace(recordValueString(target["provider"]))
		if provider != "" {
			label += " (" + provider + ")"
		}
		options = append(options, ui.SelectOption{Value: value, Label: label})
	}
	if len(options) == 0 {
		return "", ProjectError{Msg: "No selectable targets found."}
	}
	return siteAddSelectFn("Choose a target", options)
}

func siteAddTargetPickerValue(target map[string]any) string {
	return firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
}

func promptSiteAddSite(target string) (string, error) {
	for {
		prompted, err := siteAddPromptStringFn("Site name", "", false)
		if err != nil {
			return "", err
		}
		site := strings.TrimSpace(prompted)
		if err := validateSiteAddSlug(site); err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if err := validateSiteAddSiteAvailable(target, site); err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		return site, nil
	}
}

func validateSiteAddSiteAvailable(targetRef, site string) error {
	siteID, err := siteAddSiteIDForTarget(targetRef, site)
	if err != nil {
		return err
	}
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		return err
	}
	return ensureSiteNotCached(existing, siteID)
}

func siteAddSiteIDForTarget(targetRef, site string) (string, error) {
	target, provider, err := siteAddTargetRecord(targetRef)
	if err != nil {
		return "", err
	}
	if provider == "kinsta" {
		return kinstaSiteID(site), nil
	}
	targetName := firstRecordString(target, "target_name", "name", "slug", "label", "_state_key")
	if targetName == "" {
		return "", ProjectError{Msg: fmt.Sprintf("Target %q is missing a name.", targetRef)}
	}
	return linodeSiteID(site, targetName), nil
}

func siteAddTargetRecord(targetRef string) (map[string]any, string, error) {
	targets, err := cachedTargets()
	if err != nil {
		return nil, "", err
	}
	target := state.MatchingRecord(targets, targetRef)
	if target == nil {
		return nil, "", ProjectError{Msg: fmt.Sprintf("No target matched %q.", targetRef)}
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
	if provider == "" {
		return nil, "", ProjectError{Msg: fmt.Sprintf("Target %q is missing provider.", targetRef)}
	}
	return target, provider, nil
}

func promptSiteAddKinstaSlugIfNeeded(args siteAddArgs) (string, error) {
	defaultSlug := strings.TrimSpace(args.site)
	if err := validateSiteAddKinstaSlugAvailable(args.target, defaultSlug); err == nil {
		return "", nil
	} else {
		if !isKinstaSlugRetryError(err) {
			return "", err
		}
		fmt.Fprintln(os.Stderr, err)
	}
	for {
		prompted, err := siteAddPromptStringFn("Kinsta slug", defaultSlug, false)
		if err != nil {
			return "", err
		}
		kinstaSlug := strings.TrimSpace(prompted)
		if kinstaSlug == "" {
			kinstaSlug = defaultSlug
		}
		if err := validateKinstaSlug(kinstaSlug); err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if err := validateSiteAddKinstaSlugAvailable(args.target, kinstaSlug); err != nil {
			if !isKinstaSlugRetryError(err) {
				return "", err
			}
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		return kinstaSlug, nil
	}
}

func validateSiteAddKinstaSlugAvailable(targetRef, kinstaSlug string) error {
	if err := validateKinstaSlug(kinstaSlug); err != nil {
		return kinstaSlugRetryError{err: err}
	}
	target, provider, err := siteAddTargetRecord(targetRef)
	if err != nil {
		return err
	}
	if provider != "kinsta" {
		return ProjectError{Msg: fmt.Sprintf("Unsupported provider %q. Only kinsta site add slug validation is available.", provider)}
	}
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	client := newKinstaClient(token)
	ctx := context.Background()
	companyID, err := resolveKinstaCompanyID(ctx, client, firstRecordString(target, "company_id", "company"))
	if err != nil {
		return err
	}
	_, _, err = findOrValidateKinstaSiteSlug(ctx, client, companyID, kinstaSlug)
	return err
}

func promptSiteAddWithStaging() (bool, error) {
	selected, err := siteAddSelectFn("Create a staging environment too", []ui.SelectOption{
		{Value: "yes", Label: "Yes"},
		{Value: "no", Label: "No", Default: true},
	})
	if err != nil {
		return false, err
	}
	return selected == "yes", nil
}
