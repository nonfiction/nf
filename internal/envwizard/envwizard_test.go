package envwizard

import (
	"os"
	"strings"
	"testing"

	"github.com/nonfiction/nf/internal/config"
)

func TestEnsureMissingNonInteractive(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	err := Ensure([]Requirement{{Keys: []string{"NF_SECRET_SALT"}, Secret: true, Required: true}}, true)
	if err == nil {
		t.Fatal("Ensure() error = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "NF_SECRET_SALT") {
		t.Fatalf("Ensure() error = %q, want NF_SECRET_SALT in message", msg)
	}
	if !strings.Contains(msg, config.EnvFile()) {
		t.Fatalf("Ensure() error = %q, want config path in message", msg)
	}
}

func TestEnsureDoesNotWriteWhenEnvAlreadySatisfies(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_SECRET_SALT", "test-salt")
	if err := Ensure([]Requirement{{Keys: []string{"NF_SECRET_SALT"}, Secret: true, Required: true}}, true); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if _, err := os.Stat(config.EnvFile()); !os.IsNotExist(err) {
		t.Fatalf("EnvFile() existence = %v, want not exist", err)
	}
}

func TestEnsureIgnoresMissingOptionalRequirements(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	if err := Ensure([]Requirement{{Keys: []string{"DNSIMPLE_ACCOUNT_ID"}, Default: "14"}}, true); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
}

func TestInitNonInteractiveWritesOptionalDefaults(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	if err := Init([]Requirement{{Keys: []string{"DNSIMPLE_ACCOUNT_ID"}, Default: "14", WriteKey: "DNSIMPLE_ACCOUNT_ID"}}, true); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	values, err := config.ReadEnvFile(config.EnvFile())
	if err != nil {
		t.Fatalf("ReadEnvFile() error = %v", err)
	}
	if got, want := values["DNSIMPLE_ACCOUNT_ID"], "14"; got != want {
		t.Fatalf("DNSIMPLE_ACCOUNT_ID = %q, want %q", got, want)
	}
}
