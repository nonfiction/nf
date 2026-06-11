package target

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/nonfiction/nf/internal/target/provision"
)

func runAddLinode(argv []string, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "target add takes provider and name: nf target add linode <name>")
		return 1
	}
	args := provision.Args{Provider: "linode", DnsProvider: "dnsimple", Name: argv[0], TargetMode: true}
	fs := flag.NewFlagSet("target add linode", flag.ContinueOnError)
	fs.StringVar(&args.Region, "region", "", "Linode region")
	fs.StringVar(&args.Type, "type", "", "Linode type")
	fs.StringVar(&args.Image, "image", "", "Linode image")
	fs.StringVar(&args.UbuntuVersion, "ubuntu-version", "", "Ubuntu LTS version to use")
	fs.StringVar(&args.Firewall, "firewall", "", "Linode cloud firewall mode (managed or none)")
	fs.StringVar(&args.FirewallID, "firewall-id", "", "existing Linode cloud firewall id")
	fs.StringVar(&args.AdminerUser, "adminer-user", "", "Adminer database and HTTP Basic auth user")
	fs.StringVar(&args.SshUser, "ssh-user", "", "deployment SSH user")
	fs.StringVar(&args.SshUser, "user", "", "deployment SSH user")
	fs.StringVar(&args.SshKeySource, "ssh-key-source", "", "SSH key source (linode-profile or file)")
	keys := fs.String("keys", "", "SSH keys to use (all)")
	fs.StringVar(&args.SshKeyLabel, "ssh-key-label", "", "filter Linode profile SSH keys by label")
	fs.StringVar(&args.SshKeyID, "ssh-key-id", "", "filter Linode profile SSH keys by id")
	fs.BoolVar(&args.AllLinodeSshKeys, "all-linode-ssh-keys", false, "use all Linode profile SSH keys")
	fs.StringVar(&args.SshPublicKeyFile, "ssh-public-key-file", "", "SSH public key file")
	fs.StringVar(&args.WriteCloudInit, "write-cloud-init", "", "write cloud-init preview to a file")
	fs.BoolVar(&args.Wait, "wait", false, "wait for SSH, TLS, and health checks")
	fs.BoolVar(&args.NoWait, "no-wait", false, "skip SSH, TLS, and health checks")
	fs.BoolVar(&args.NonInteractive, "non-interactive", false, "")
	fs.BoolVar(&args.ShowCloudInit, "show-cloud-init", false, "show cloud-init preview")
	fs.BoolVar(&args.Execute, "execute", false, "execute remote provisioning")
	fs.BoolVar(&args.Yes, "yes", false, "confirm execution in non-interactive mode")
	fs.BoolVar(&args.DryRun, "dry-run", false, "show the plan without executing")
	fs.SetOutput(stderr)
	if err := fs.Parse(argv[1:]); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "target add linode takes exactly one name")
		return 1
	}
	if strings.TrimSpace(*keys) != "" {
		switch strings.ToLower(strings.TrimSpace(*keys)) {
		case "all":
			args.AllLinodeSshKeys = true
		case "":
		default:
			fmt.Fprintln(stderr, "target add linode --keys supports only all in this slice")
			return 1
		}
	}
	if args.Execute && args.DryRun {
		fmt.Fprintln(stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if args.Wait && args.NoWait {
		fmt.Fprintln(stderr, "Choose either --wait or --no-wait, not both.")
		return 1
	}
	if !args.Execute {
		args.DryRun = true
	}
	if !args.Wait {
		args.NoWait = true
	}
	if args.NonInteractive && args.Execute && !args.Yes {
		fmt.Fprintln(stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	plan, err := provision.BuildPlan(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, err = provision.ProvisionServer(plan)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
