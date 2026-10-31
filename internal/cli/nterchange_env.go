package cli

import (
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"

	"github.com/nonfiction/nf/internal/config"
)

// Resolve the existing recipe without contacting the daemon. Never print its
// JSON: interpolated environment values can contain application credentials.
type nterchangeComposeConfig struct {
	Name     string                              `json:"name"`
	Services map[string]nterchangeComposeService `json:"services"`
	Networks map[string]struct {
		Internal bool `json:"internal"`
	} `json:"networks"`
}

type nterchangeComposeService struct {
	Image       string                  `json:"image,omitempty"`
	Command     []string                `json:"command,omitempty"`
	Environment map[string]string       `json:"environment,omitempty"`
	Ports       []nterchangeComposePort `json:"ports,omitempty"`
	Networks    map[string]any          `json:"networks,omitempty"`
}

type nterchangeComposePort struct {
	HostIP    string `json:"host_ip"`
	Published string `json:"published"`
	Target    int    `json:"target"`
	Protocol  string `json:"protocol"`
}

// Only this service and its egress network enter the override. Never serialize
// the resolved base configuration: it contains credentials and expanded binds.
func (cfg nterchangeComposeConfig) withDatabaseUI() (nterchangeComposeConfig, string, []byte, error) {
	if _, exists := cfg.Services["db-ui"]; exists {
		return cfg, "", nil, nil
	}
	_, _, port := envDerivedPorts("nterchange-" + cfg.Name)
	if _, exists := cfg.Networks["nf-db-ui"]; exists {
		return cfg, "", nil, fmt.Errorf("Compose network nf-db-ui is reserved for nf's database UI")
	}
	networks := map[string]any{"nf-db-ui": nil}
	for name := range cfg.Services["db"].Networks {
		// Do not copy db's static addresses or DNS aliases onto the UI.
		networks[name] = nil
	}
	service := nterchangeComposeService{
		Image:    defaultDockerWordpressImage,
		Command:  []string{"sh", "-lc", `mkdir -p /var/www/html && php -r 'copy("` + localAdminNeoURL + `", "/var/www/html/index.php") || exit(1);' && php -S 0.0.0.0:80 -t /var/www/html`},
		Ports:    []nterchangeComposePort{{HostIP: "127.0.0.1", Published: strconv.Itoa(port), Target: 80, Protocol: "tcp"}},
		Networks: networks,
	}
	override := map[string]any{
		"services": map[string]any{"db-ui": service},
		"networks": map[string]any{"nf-db-ui": map[string]any{}},
	}
	data, err := json.MarshalIndent(override, "", "  ")
	if err != nil {
		return cfg, "", nil, err
	}
	cfg.Services = maps.Clone(cfg.Services)
	cfg.Services["db-ui"] = service
	file := filepath.Join(config.DataHome(), "nterchange", "envs", cleanEnvSlug(cfg.Name), "db-ui.compose.json")
	return cfg, file, append(data, '\n'), nil
}

func nterchangeComposeArgs(root string, args ...string) []string {
	return append([]string{"docker", "compose", "-f", filepath.Join(root, "compose.yaml")}, args...)
}

func loadNterchangeCompose(root string) (nterchangeComposeConfig, error) {
	var cfg nterchangeComposeConfig
	if !fileExists(filepath.Join(root, "compose.yaml")) {
		return cfg, fmt.Errorf("nterchange env requires the site's existing compose.yaml")
	}
	// Discard Compose diagnostics as well as raw JSON on error. Either can
	// include expanded secrets; report only the failed command and exit status.
	output, err := runCommandSpecOutputQuiet(execSpec{Dir: root, Args: nterchangeComposeArgs(root, "config", "--format", "json")})
	if err != nil {
		return cfg, fmt.Errorf("cannot resolve nterchange Compose configuration: %w", err)
	}
	if json.Unmarshal([]byte(output), &cfg) != nil || cfg.Name == "" {
		return cfg, fmt.Errorf("invalid nterchange Compose configuration")
	}
	for _, service := range []string{"app", "db"} {
		if _, ok := cfg.Services[service]; !ok {
			return cfg, fmt.Errorf("nterchange Compose configuration requires an %s service", service)
		}
	}
	return cfg, nil
}

func (cfg nterchangeComposeConfig) validateLocalStart() error {
	for _, name := range []string{"app", "db"} {
		service := cfg.Services[name]
		if len(service.Networks) == 0 {
			return fmt.Errorf("nterchange local %s must use internal Compose networks", name)
		}
		for network := range service.Networks {
			if !cfg.Networks[network].Internal {
				return fmt.Errorf("nterchange local %s must use only internal Compose networks", name)
			}
		}
	}
	for name, service := range cfg.Services {
		for _, port := range service.Ports {
			ip := net.ParseIP(port.HostIP)
			if ip == nil || !ip.IsLoopback() {
				return fmt.Errorf("nterchange local %s published ports must bind a loopback address", name)
			}
		}
	}
	return nil
}

func (cfg nterchangeComposeConfig) serviceNames() []string {
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (cfg nterchangeComposeConfig) portRows() []detailRow {
	var rows []detailRow
	for _, name := range cfg.serviceNames() {
		service := cfg.Services[name]
		ports := slices.Clone(service.Ports)
		sort.Slice(ports, func(i, j int) bool {
			a, b := ports[i], ports[j]
			if a.Target != b.Target {
				return a.Target < b.Target
			}
			return a.HostIP+":"+a.Published+"/"+a.Protocol < b.HostIP+":"+b.Published+"/"+b.Protocol
		})
		for _, port := range ports {
			ip := net.ParseIP(port.HostIP)
			if ip == nil || !ip.IsLoopback() {
				continue
			}
			label := name + ":" + strconv.Itoa(port.Target)
			address := net.JoinHostPort(port.HostIP, port.Published)
			if port.Protocol == "tcp" && (port.Target == 80 || port.Target == 8080 || port.Target == 8025) {
				address = "http://" + address
			}
			if port.Protocol == "tcp" {
				switch {
				case (name == "app" || name == "gateway") && (port.Target == 80 || port.Target == 8080):
					label = "Site URL"
				case (name == "mailpit" || name == "gateway") && port.Target == 8025:
					label = "Mailpit URL"
				case name == "db-ui" && (port.Target == 80 || port.Target == 8080):
					label = "DB URL"
					env := cfg.Services["db"].Environment
					query := url.Values{"mysql": {"db"}}
					if env["MYSQL_USER"] != "" {
						query.Set("username", env["MYSQL_USER"])
					}
					if env["MYSQL_DATABASE"] != "" {
						query.Set("db", env["MYSQL_DATABASE"])
					}
					address += "/?" + query.Encode()
				}
			}
			rows = append(rows, detailRow{label: label, value: address})
		}
	}
	return rows
}

func cmdNterchangeLocalEnv(root string, metadata *projectMetadata, name string, args []string) int {
	rebuild := false
	if name == "up" && len(args) == 1 && args[0] == "--rebuild" {
		rebuild = true
	} else if len(args) != 0 {
		fmt.Fprintf(os.Stderr, "nterchange env %s does not accept these arguments; logs and shell are local only\n", name)
		return 1
	}
	cfg, err := loadNterchangeCompose(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if name == "up" {
		if err := cfg.validateLocalStart(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	var override string
	var data []byte
	if name == "show" || name == "up" || name == "down" {
		cfg, override, data, err = cfg.withDatabaseUI()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	composeArgs := nterchangeComposeArgs(root)
	if override != "" && (name == "up" || name == "down") {
		if err = os.MkdirAll(filepath.Dir(override), 0700); err == nil {
			err = os.WriteFile(override, data, 0600)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		composeArgs = append(composeArgs, "-f", override)
	}
	run := func(args ...string) error {
		return runCommandSpec(execSpec{Dir: root, Args: append(slices.Clone(composeArgs), args...)})
	}
	switch name {
	case "up":
		if rebuild {
			err = run("build", "app")
		}
		if err == nil && override != "" {
			err = run("pull", "--policy", "missing", "db-ui")
		}
		if err == nil {
			err = run("up", "-d", "--no-build", "--pull", "never")
		}
	case "down":
		err = run("down")
	case "logs":
		err = run("logs", "-f", "app")
	case "shell":
		err = run("exec", "app", "sh")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if name == "up" || name == "down" {
		if name == "up" {
			fmt.Println("Env started.")
		} else {
			fmt.Println("Env stopped.")
		}
		fmt.Println()
	}
	if name == "show" || name == "up" || name == "down" {
		fmt.Println(renderNterchangeEnvInfo(root, metadata, cfg, name != "down"))
	}
	return 0
}
