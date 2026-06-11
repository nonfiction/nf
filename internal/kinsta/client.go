package kinsta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Error struct{ Msg string }

func (e Error) Error() string { return e.Msg }

func rawString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type Option func(*Client)

func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

func NewClient(baseURL, token string, opts ...Option) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.kinsta.com/v2"
	}
	c := &Client{baseURL: baseURL, token: strings.TrimSpace(token), httpClient: http.DefaultClient}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type ValidateResponse struct {
	Name    string `json:"name"`
	Company string `json:"company"`
	Status  string `json:"status"`
}

type Operation struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (o *Operation) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	o.ID = rawString(raw["id"])
	o.Status = rawString(raw["status"])
	return nil
}

type Site struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

type Environment struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	DisplayName   string        `json:"display_name"`
	PHPVersion    string        `json:"php_version"`
	PHP           string        `json:"php"`
	IsPremium     bool          `json:"is_premium"`
	WebRoot       string        `json:"web_root"`
	Domains       []Domain      `json:"domains"`
	PrimaryDomain Domain        `json:"primaryDomain"`
	SSHConnection SSHConnection `json:"ssh_connection"`
	ContainerInfo ContainerInfo `json:"container_info"`
}

func (e Environment) CurrentPHPVersion() string {
	for _, value := range []string{e.PHPVersion, e.PHP, e.ContainerInfo.PHPEngineVersion} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		return strings.TrimPrefix(value, "php")
	}
	return ""
}

type SSHConnection struct {
	SSHPort string `json:"ssh_port"`
	SSHIP   struct {
		ExternalIP string `json:"external_ip"`
	} `json:"ssh_ip"`
}

type ContainerInfo struct {
	ID               string `json:"id"`
	PHPEngineVersion string `json:"php_engine_version"`
}

type SFTPConfig struct {
	Host       string
	Port       string
	User       string
	Name       string
	SSHCommand string
}

type SFTPPassword struct {
	EnvironmentID string
	Password      string
}

type Domain struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Domain     string `json:"domain"`
	DomainName string `json:"domain_name"`
	Type       string `json:"type"`
	IsPrimary  bool   `json:"is_primary"`
}

type DNSRecord struct {
	Name        string `json:"name"`
	Hostname    string `json:"hostname"`
	Type        string `json:"type"`
	RecordType  string `json:"record_type"`
	Content     string `json:"content"`
	Value       string `json:"value"`
	RecordValue string `json:"record_value"`
	TTL         int    `json:"ttl"`
}

func (r DNSRecord) RecordName() string {
	if strings.TrimSpace(r.Name) != "" {
		return strings.TrimSpace(r.Name)
	}
	return strings.TrimSpace(r.Hostname)
}

func (r DNSRecord) RecordTypeName() string {
	if strings.TrimSpace(r.Type) != "" {
		return strings.TrimSpace(r.Type)
	}
	return strings.TrimSpace(r.RecordType)
}

func (r DNSRecord) RecordContent() string {
	if strings.TrimSpace(r.Content) != "" {
		return strings.TrimSpace(r.Content)
	}
	if strings.TrimSpace(r.Value) != "" {
		return strings.TrimSpace(r.Value)
	}
	return strings.TrimSpace(r.RecordValue)
}

type DomainRecords struct {
	Verification []DNSRecord
	Pointing     []DNSRecord
}

type CreateSiteRequest struct {
	Company              string `json:"company"`
	DisplayName          string `json:"display_name"`
	Region               string `json:"region"`
	InstallMode          string `json:"install_mode"`
	AdminEmail           string `json:"admin_email"`
	AdminPassword        string `json:"admin_password"`
	AdminUser            string `json:"admin_user"`
	SiteTitle            string `json:"site_title"`
	WPLanguage           string `json:"wp_language"`
	IsSubdomainMultisite bool   `json:"is_subdomain_multisite"`
	IsMultisite          bool   `json:"is_multisite"`
	WooCommerce          bool   `json:"woocommerce"`
	WordPressSEO         bool   `json:"wordpressseo"`
}

type CloneEnvironmentRequest struct {
	DisplayName string `json:"display_name"`
	IsPremium   bool   `json:"is_premium"`
	SourceEnvID string `json:"source_env_id"`
}

type AddDomainRequest struct {
	DomainName          string `json:"domain_name"`
	IsWildcardless      bool   `json:"is_wildcardless"`
	AddWithWWWSubdomain bool   `json:"add_with_www_subdomain"`
	SetupType           string `json:"setup_type"`
}

type ModifyPHPVersionRequest struct {
	EnvironmentID                  string `json:"environment_id"`
	PHPVersion                     string `json:"php_version"`
	IsOptOutFromAutomaticPHPUpdate bool   `json:"is_opt_out_from_automatic_php_update"`
}

func (c *Client) Validate(ctx context.Context) (ValidateResponse, error) {
	var out ValidateResponse
	return out, c.do(ctx, http.MethodGet, "/validate", nil, &out)
}

func (c *Client) CreateSite(ctx context.Context, req CreateSiteRequest) (string, error) {
	var out operationResponse
	if err := c.do(ctx, http.MethodPost, "/sites", req, &out); err != nil {
		return "", err
	}
	return out.OperationID(), nil
}

func (c *Client) ListSites(ctx context.Context, companyID string) ([]Site, error) {
	path := "/sites"
	if strings.TrimSpace(companyID) != "" {
		path += "?company=" + url.QueryEscape(strings.TrimSpace(companyID))
	}
	var out sitesResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Sites(), nil
}

func (c *Client) ListEnvironments(ctx context.Context, siteID string) ([]Environment, error) {
	var out environmentsResponse
	if err := c.do(ctx, http.MethodGet, "/sites/"+url.PathEscape(siteID)+"/environments", nil, &out); err != nil {
		return nil, err
	}
	return out.Environments(), nil
}

func (c *Client) CloneEnvironment(ctx context.Context, siteID string, req CloneEnvironmentRequest) (string, error) {
	var out operationResponse
	if err := c.do(ctx, http.MethodPost, "/sites/"+url.PathEscape(siteID)+"/environments/clone", req, &out); err != nil {
		return "", err
	}
	return out.OperationID(), nil
}

func (c *Client) DeleteEnvironment(ctx context.Context, envID string) (string, error) {
	var out operationResponse
	if err := c.do(ctx, http.MethodDelete, "/sites/environments/"+url.PathEscape(envID), nil, &out); err != nil {
		return "", err
	}
	return out.OperationID(), nil
}

func (c *Client) DeleteSite(ctx context.Context, siteID string) (string, error) {
	var out operationResponse
	if err := c.do(ctx, http.MethodDelete, "/sites/"+url.PathEscape(siteID), nil, &out); err != nil {
		return "", err
	}
	return out.OperationID(), nil
}

func (c *Client) AddDomain(ctx context.Context, envID string, req AddDomainRequest) (string, error) {
	var out operationResponse
	if err := c.do(ctx, http.MethodPost, "/sites/environments/"+url.PathEscape(envID)+"/domains", req, &out); err != nil {
		return "", err
	}
	return out.OperationID(), nil
}

func (c *Client) ListDomains(ctx context.Context, envID string) ([]Domain, error) {
	var out domainsResponse
	if err := c.do(ctx, http.MethodGet, "/sites/environments/"+url.PathEscape(envID)+"/domains", nil, &out); err != nil {
		return nil, err
	}
	return out.Domains(), nil
}

func (c *Client) SFTPConfig(ctx context.Context, siteID, envID string) (SFTPConfig, error) {
	var out sftpConfigResponse
	if err := c.do(ctx, http.MethodGet, "/sites/"+url.PathEscape(siteID)+"/environments/"+url.PathEscape(envID)+"/ssh/config", nil, &out); err != nil {
		return SFTPConfig{}, err
	}
	return out.Config(), nil
}

func (c *Client) SFTPPassword(ctx context.Context, envID string) (SFTPPassword, error) {
	var out sftpPasswordResponse
	if err := c.do(ctx, http.MethodGet, "/sites/environments/"+url.PathEscape(envID)+"/ssh/password", nil, &out); err != nil {
		return SFTPPassword{}, err
	}
	return out.Password(), nil
}

func (c *Client) DomainRecords(ctx context.Context, domainID string) (DomainRecords, error) {
	var out domainRecordsResponse
	if err := c.do(ctx, http.MethodGet, "/sites/environments/domains/"+url.PathEscape(domainID)+"/verification-records", nil, &out); err != nil {
		return DomainRecords{}, err
	}
	return out.Records(), nil
}

func (c *Client) ChangePrimaryDomain(ctx context.Context, envID, domainID string, runSearchReplace bool) (string, error) {
	var out operationResponse
	payload := map[string]any{"domain_id": domainID, "run_search_and_replace": runSearchReplace}
	if err := c.do(ctx, http.MethodPut, "/sites/environments/"+url.PathEscape(envID)+"/change-primary-domain", payload, &out); err != nil {
		return "", err
	}
	return out.OperationID(), nil
}

func (c *Client) ModifyPHPVersion(ctx context.Context, req ModifyPHPVersionRequest) (string, error) {
	var out operationResponse
	if err := c.do(ctx, http.MethodPut, "/sites/tools/modify-php-version", req, &out); err != nil {
		return "", err
	}
	return out.OperationID(), nil
}

func (c *Client) Operation(ctx context.Context, operationID string) (Operation, error) {
	var out operationStatusResponse
	if err := c.do(ctx, http.MethodGet, "/operations/"+url.PathEscape(operationID), nil, &out); err != nil {
		return Operation{}, err
	}
	return out.Operation(), nil
}

func (c *Client) WaitOperation(ctx context.Context, operationID string, interval time.Duration) error {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		op, err := c.Operation(ctx, operationID)
		if err != nil {
			return err
		}
		status := strings.ToLower(strings.TrimSpace(op.Status))
		switch status {
		case "", "202", "queued", "pending", "processing", "running", "in_progress":
		case "200", "complete", "completed", "success", "succeeded", "finished", "done":
			return nil
		case "failed", "failure", "error", "cancelled", "canceled":
			return Error{Msg: fmt.Sprintf("Kinsta operation %s failed with status %q", operationID, op.Status)}
		default:
			// Unknown non-empty statuses are treated as still running. Kinsta has changed
			// operation labels before; this keeps polling conservative.
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Error{Msg: fmt.Sprintf("Timed out waiting for Kinsta operation %s: %v", operationID, ctx.Err())}
		case <-timer.C:
		}
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Error{Msg: fmt.Sprintf("Kinsta %s %s returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))}
	}
	if out == nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

type operationResponse struct {
	OperationIDValue string    `json:"operation_id"`
	ID               string    `json:"id"`
	OperationValue   Operation `json:"operation"`
}

func (r operationResponse) OperationID() string {
	if strings.TrimSpace(r.OperationIDValue) != "" {
		return strings.TrimSpace(r.OperationIDValue)
	}
	if strings.TrimSpace(r.OperationValue.ID) != "" {
		return strings.TrimSpace(r.OperationValue.ID)
	}
	return strings.TrimSpace(r.ID)
}

type operationStatusResponse struct {
	OperationValue Operation `json:"operation"`
	ID             string    `json:"id"`
	Status         any       `json:"status"`
}

func (r operationStatusResponse) Operation() Operation {
	if r.OperationValue.ID != "" || r.OperationValue.Status != "" {
		return r.OperationValue
	}
	return Operation{ID: r.ID, Status: rawString(r.Status)}
}

type sitesResponse struct {
	Company struct {
		Sites []Site `json:"sites"`
	} `json:"company"`
	SitesValue []Site `json:"sites"`
}

func (r sitesResponse) Sites() []Site {
	if len(r.Company.Sites) > 0 {
		return r.Company.Sites
	}
	return r.SitesValue
}

type environmentsResponse struct {
	Site struct {
		Environments []Environment `json:"environments"`
	} `json:"site"`
	EnvironmentsValue []Environment `json:"environments"`
}

func (r environmentsResponse) Environments() []Environment {
	if len(r.Site.Environments) > 0 {
		return r.Site.Environments
	}
	return r.EnvironmentsValue
}

type domainsResponse struct {
	Environment struct {
		SiteDomains []Domain `json:"site_domains"`
	} `json:"environment"`
	SiteDomains  []Domain `json:"site_domains"`
	DomainsValue []Domain `json:"domains"`
}

type sftpConfigResponse struct {
	Port       string `json:"port"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	User       string `json:"user"`
	SSHCommand string `json:"ssh_command"`
	Site       struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		User        string `json:"usr"`
		Environment struct {
			ActiveContainer struct {
				LXDSshPort   string `json:"lxdSshPort"`
				LoadBalancer struct {
					ExtIP string `json:"extIP"`
				} `json:"loadBalancer"`
			} `json:"activeContainer"`
		} `json:"environment"`
	} `json:"site"`
}

type sftpPasswordResponse struct {
	Environment struct {
		ID           string `json:"id"`
		SFTPPassword string `json:"sftp_password"`
	} `json:"environment"`
}

func (r sftpPasswordResponse) Password() SFTPPassword {
	return SFTPPassword{EnvironmentID: strings.TrimSpace(r.Environment.ID), Password: strings.TrimSpace(r.Environment.SFTPPassword)}
}

func (r sftpConfigResponse) Config() SFTPConfig {
	cfg := SFTPConfig{Host: strings.TrimSpace(r.Host), Port: strings.TrimSpace(r.Port), User: strings.TrimSpace(r.User), Name: strings.TrimSpace(r.Name), SSHCommand: strings.TrimSpace(r.SSHCommand)}
	if cfg.Host == "" {
		cfg.Host = strings.TrimSpace(r.Site.Environment.ActiveContainer.LoadBalancer.ExtIP)
	}
	if cfg.Port == "" {
		cfg.Port = strings.TrimSpace(r.Site.Environment.ActiveContainer.LXDSshPort)
	}
	if cfg.User == "" {
		cfg.User = strings.TrimSpace(r.Site.User)
	}
	if cfg.Name == "" {
		cfg.Name = strings.TrimSpace(r.Site.Name)
	}
	return cfg
}

func (r domainsResponse) Domains() []Domain {
	if len(r.Environment.SiteDomains) > 0 {
		return r.Environment.SiteDomains
	}
	if len(r.SiteDomains) > 0 {
		return r.SiteDomains
	}
	return r.DomainsValue
}

type domainRecordsResponse struct {
	SiteDomain struct {
		VerificationRecords []DNSRecord `json:"verification_records"`
		PointingRecords     []DNSRecord `json:"pointing_records"`
	} `json:"site_domain"`
	VerificationRecords []DNSRecord `json:"verification_records"`
	PointingRecords     []DNSRecord `json:"pointing_records"`
}

func (r domainRecordsResponse) Records() DomainRecords {
	out := DomainRecords{Verification: r.VerificationRecords, Pointing: r.PointingRecords}
	if len(r.SiteDomain.VerificationRecords) > 0 {
		out.Verification = r.SiteDomain.VerificationRecords
	}
	if len(r.SiteDomain.PointingRecords) > 0 {
		out.Pointing = r.SiteDomain.PointingRecords
	}
	return out
}

func FindSite(sites []Site, name string) (Site, bool) {
	needle := strings.ToLower(strings.TrimSpace(name))
	for _, site := range sites {
		for _, candidate := range []string{site.ID, site.Name, site.DisplayName} {
			if strings.ToLower(strings.TrimSpace(candidate)) == needle {
				return site, true
			}
		}
	}
	return Site{}, false
}

func FindEnvironment(envs []Environment, name string) (Environment, bool) {
	needle := strings.ToLower(strings.TrimSpace(name))
	for _, env := range envs {
		for _, candidate := range []string{env.ID, env.Name, env.DisplayName} {
			if strings.ToLower(strings.TrimSpace(candidate)) == needle {
				return env, true
			}
		}
	}
	return Environment{}, false
}

func FindDomain(domains []Domain, name string) (Domain, bool) {
	needle := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	for _, domain := range domains {
		for _, candidate := range []string{domain.ID, domain.Name, domain.Domain, domain.DomainName} {
			if strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), ".")) == needle {
				return domain, true
			}
		}
	}
	return Domain{}, false
}
