package envwizard

import (
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/ui"
)

type Requirement struct {
	Keys     []string
	Prompt   string
	Default  string
	Secret   bool
	WriteKey string
	Required bool
}

func (r Requirement) label() string {
	return strings.Join(r.Keys, " or ")
}

func (r Requirement) preferredWriteKey() string {
	if strings.TrimSpace(r.WriteKey) != "" {
		return r.WriteKey
	}
	if len(r.Keys) > 0 {
		return r.Keys[0]
	}
	return ""
}

func Value(name string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	values, err := config.ReadEnvFile(config.EnvFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(values[name])
}

func requirementValue(req Requirement) string {
	for _, key := range req.Keys {
		if v := Value(key); v != "" {
			return v
		}
	}
	return ""
}

func missingRequirements(reqs []Requirement, includeOptional bool) []Requirement {
	missing := make([]Requirement, 0, len(reqs))
	for _, req := range reqs {
		if !includeOptional && !req.Required {
			continue
		}
		if requirementValue(req) == "" {
			missing = append(missing, req)
		}
	}
	return missing
}

func isInteractiveTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func missingMessage(path string, reqs []Requirement) string {
	parts := make([]string, 0, len(reqs))
	for _, req := range reqs {
		parts = append(parts, req.label())
	}
	if len(parts) == 1 {
		return fmt.Sprintf("Missing %s. It is not set in the environment or %s.", parts[0], path)
	}
	return fmt.Sprintf("Missing required values (%s). They are not set in the environment or %s.", strings.Join(parts, ", "), path)
}

func promptAndWrite(reqs []Requirement) error {
	updates := map[string]string{}
	for _, req := range reqs {
		if requirementValue(req) != "" {
			continue
		}
		prompt := req.Prompt
		if strings.TrimSpace(prompt) == "" {
			prompt = req.label() + ": "
		}
		var value string
		var err error
		if req.Secret {
			value, err = ui.PromptSecret(prompt)
		} else {
			value, err = ui.PromptString(prompt, req.Default, false)
		}
		if err != nil {
			return err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = strings.TrimSpace(req.Default)
		}
		if value == "" && req.Required {
			return fmt.Errorf("%s is required", req.label())
		}
		if value == "" {
			continue
		}
		writeKey := req.preferredWriteKey()
		if writeKey != "" {
			updates[writeKey] = value
		}
	}
	if len(updates) == 0 {
		return nil
	}
	written, err := config.UpdateEnvFile(config.EnvFile(), updates)
	if err != nil {
		return err
	}
	if len(written) > 0 {
		fmt.Printf("Updated %s\n", config.EnvFile())
	}
	return nil
}

func Ensure(reqs []Requirement, nonInteractive bool) error {
	missing := missingRequirements(reqs, false)
	if len(missing) == 0 {
		return nil
	}
	if nonInteractive || !isInteractiveTerminal() {
		return fmt.Errorf("%s\nRun `nf config init` to populate it.", missingMessage(config.EnvFile(), missing))
	}
	answer, err := ui.Confirm("Missing config values were found. Populate "+config.EnvFile()+" now?", true)
	if err != nil {
		return err
	}
	if !answer {
		return fmt.Errorf("%s\nRun `nf config init` to populate it.", missingMessage(config.EnvFile(), missing))
	}
	if err := promptAndWrite(missing); err != nil {
		return err
	}
	return nil
}

func Init(reqs []Requirement, nonInteractive bool) error {
	missing := missingRequirements(reqs, true)
	if len(missing) == 0 {
		return nil
	}
	if nonInteractive || !isInteractiveTerminal() {
		requiredMissing := missingRequirements(reqs, false)
		if len(requiredMissing) > 0 {
			return fmt.Errorf("%s\nRun `nf config init` interactively to populate it.", missingMessage(config.EnvFile(), requiredMissing))
		}
		updates := map[string]string{}
		for _, req := range missing {
			if req.Secret || strings.TrimSpace(req.Default) == "" {
				continue
			}
			if writeKey := req.preferredWriteKey(); writeKey != "" {
				updates[writeKey] = strings.TrimSpace(req.Default)
			}
		}
		_, err := config.UpdateEnvFile(config.EnvFile(), updates)
		return err
	}
	return promptAndWrite(missing)
}
