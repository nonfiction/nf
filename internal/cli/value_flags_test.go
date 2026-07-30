package cli

import (
	"fmt"
	"reflect"
	"testing"
)

func TestNormalizeLongFlagValues(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		flags []string
		want  []string
	}{
		{
			name:  "equals",
			args:  []string{"name", "--for=local"},
			flags: []string{"--for"},
			want:  []string{"name", "--for", "local"},
		},
		{
			name:  "value contains equals",
			args:  []string{"--note=left=right"},
			flags: []string{"--note"},
			want:  []string{"--note", "left=right"},
		},
		{
			name:  "empty value",
			args:  []string{"--output="},
			flags: []string{"--output"},
			want:  []string{"--output", ""},
		},
		{
			name:  "unregistered and boolean flags",
			args:  []string{"--unknown=value", "--dry-run=true"},
			flags: []string{"--output"},
			want:  []string{"--unknown=value", "--dry-run=true"},
		},
		{
			name:  "passthrough boundary",
			args:  []string{"--output=before", "--", "--output=after"},
			flags: []string{"--output"},
			want:  []string{"--output", "before", "--", "--output=after"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeLongFlagValues(test.args, test.flags...); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("normalizeLongFlagValues(%q) = %q, want %q", test.args, got, test.want)
			}
		})
	}
}

func TestManualValueFlagSyntaxParity(t *testing.T) {
	type parseFunc func([]string) (any, error)
	tests := []struct {
		name   string
		spaced []string
		equals []string
		parse  parseFunc
	}{
		{
			name:   "define get for",
			spaced: []string{"NAME", "--for", "local"},
			equals: []string{"NAME", "--for=local"},
			parse: func(args []string) (any, error) {
				return parseDefineGetPartial(args)
			},
		},
		{
			name:   "define set for",
			spaced: []string{"NAME", "value", "--for", "local"},
			equals: []string{"NAME", "value", "--for=local"},
			parse: func(args []string) (any, error) {
				return parseDefineSetPartial(args)
			},
		},
		{
			name:   "define remove for",
			spaced: []string{"NAME", "--for", "local"},
			equals: []string{"NAME", "--for=local"},
			parse: func(args []string) (any, error) {
				return parseDefineRemovePartial(args)
			},
		},
		{
			name:   "plugin add source and note",
			spaced: []string{"stream", "--source", "repo", "--note", "left=right"},
			equals: []string{"stream", "--source=repo", "--note=left=right"},
			parse: func(args []string) (any, error) {
				opts, ok := parseEnvPluginAddArgs(args)
				if !ok {
					return nil, fmt.Errorf("plugin parser rejected arguments")
				}
				return opts, nil
			},
		},
		{
			name:   "theme add source path and note",
			spaced: []string{"client", "--source", "repo", "--path", "theme", "--note", "left=right"},
			equals: []string{"client", "--source=repo", "--path=theme", "--note=left=right"},
			parse: func(args []string) (any, error) {
				opts, ok := parseEnvThemeAddArgs(args)
				if !ok {
					return nil, fmt.Errorf("theme parser rejected arguments")
				}
				return opts, nil
			},
		},
		{
			name:   "password derive version",
			spaced: []string{"project", "--password-version", "2"},
			equals: []string{"project", "--password-version=2"},
			parse: func(args []string) (any, error) {
				return parsePasswordDeriveArgs(args)
			},
		},
		{
			name:   "site snapshot output",
			spaced: []string{"client:live", "--output", "/tmp/output"},
			equals: []string{"client:live", "--output=/tmp/output"},
			parse: func(args []string) (any, error) {
				ref, opts, err := parseSiteSnapshotArgs(args)
				return []any{ref, opts}, err
			},
		},
		{
			name:   "site export output",
			spaced: []string{"client:live", "--output", "/tmp/output"},
			equals: []string{"client:live", "--output=/tmp/output"},
			parse: func(args []string) (any, error) {
				ref, opts, err := parseSiteExportArgs(args)
				return []any{ref, opts}, err
			},
		},
		{
			name:   "site snapshot prune keep",
			spaced: []string{"--keep", "4"},
			equals: []string{"--keep=4"},
			parse: func(args []string) (any, error) {
				return parseSiteSnapshotPruneArgs(args)
			},
		},
		{
			name:   "env snapshot prune keep",
			spaced: []string{"--keep", "4"},
			equals: []string{"--keep=4"},
			parse: func(args []string) (any, error) {
				return parseEnvSnapshotPruneArgs(args)
			},
		},
		{
			name:   "env snapshot import name",
			spaced: []string{"remote", "--name", "local"},
			equals: []string{"remote", "--name=local"},
			parse: func(args []string) (any, error) {
				remote, local, err := parseEnvSnapshotImportArgs(args)
				return []any{remote, local}, err
			},
		},
		{
			name:   "env snapshot use remote and name",
			spaced: []string{"--remote", "remote", "--name", "local"},
			equals: []string{"--remote=remote", "--name=local"},
			parse: func(args []string) (any, error) {
				return parseEnvSnapshotUseArgs(args)
			},
		},
		{
			name:   "env import db source url and name",
			spaced: []string{"source", "--db", "db.sql", "--source-url", "https://example.com/?a=b", "--name", "import"},
			equals: []string{"source", "--db=db.sql", "--source-url=https://example.com/?a=b", "--name=import"},
			parse: func(args []string) (any, error) {
				return parseEnvImportArgs(args)
			},
		},
		{
			name:   "site repair project slug",
			spaced: []string{"client:live", "--project-slug", "client"},
			equals: []string{"client:live", "--project-slug=client"},
			parse: func(args []string) (any, error) {
				ref, opts, err := parseSiteRepairArgs(args)
				return []any{ref, opts}, err
			},
		},
		{
			name:   "domain proxy and waits",
			spaced: []string{"client:live", "example.com", "--proxy", "cloudflare", "--wait-timeout", "30m", "--wait-interval", "30s"},
			equals: []string{"client:live", "example.com", "--proxy=cloudflare", "--wait-timeout=30m", "--wait-interval=30s"},
			parse: func(args []string) (any, error) {
				ref, opts, ok := parseSiteDomainActionArgs("primary", args)
				if !ok {
					return nil, fmt.Errorf("domain parser rejected arguments")
				}
				return []any{ref, opts}, nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spaced, err := test.parse(test.spaced)
			if err != nil {
				t.Fatalf("spaced syntax error = %v", err)
			}
			equals, err := test.parse(test.equals)
			if err != nil {
				t.Fatalf("equals syntax error = %v", err)
			}
			if !reflect.DeepEqual(equals, spaced) {
				t.Fatalf("equals result = %#v, spaced result = %#v", equals, spaced)
			}
		})
	}
}
