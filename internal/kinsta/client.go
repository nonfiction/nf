package kinsta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Error struct {
	Msg        string
	StatusCode int
}

func (e Error) Error() string { return e.Msg }

func IsTemporary(err error) bool {
	var kerr Error
	if !errors.As(err, &kerr) {
		return false
	}
	return kerr.StatusCode == http.StatusTooManyRequests || kerr.StatusCode >= 500
}

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
	graphqlURL string
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

func WithGraphQLURL(graphqlURL string) Option {
	return func(c *Client) {
		graphqlURL = strings.TrimSpace(graphqlURL)
		if graphqlURL != "" {
			c.graphqlURL = strings.TrimRight(graphqlURL, "/")
		}
	}
}

func NewClient(baseURL, token string, opts ...Option) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.kinsta.com/v2"
	}
	c := &Client{baseURL: baseURL, graphqlURL: "https://graphql-router.kinsta.com", token: strings.TrimSpace(token), httpClient: http.DefaultClient}
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
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
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
	o.Message = rawString(raw["message"])
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
	IsBlocked     bool          `json:"is_blocked"`
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
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Domain             string `json:"domain"`
	DomainName         string `json:"domain_name"`
	Type               string `json:"type"`
	Status             string `json:"status"`
	State              string `json:"state"`
	DomainStatus       string `json:"domain_status"`
	DNSStatus          string `json:"dns_status"`
	VerificationStatus string `json:"verification_status"`
	IsPrimary          bool   `json:"is_primary"`
	IsVerified         *bool  `json:"is_verified"`
	IsPointing         *bool  `json:"is_pointing"`
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

type DomainVerificationValidation struct {
	DomainID string
	Valid    bool
	Records  []DomainVerificationRecord
}

type DomainVerificationRecord struct {
	Name     string
	Value    string
	Type     string
	Detected bool
}

type GraphQLAction struct {
	ID    int
	Found bool
	Done  bool
	Error string
}

type ActivityLog struct {
	ID           int64    `json:"id"`
	SiteID       string   `json:"site_id"`
	Type         string   `json:"type"`
	Done         bool     `json:"is_done"`
	Failed       bool     `json:"has_failed"`
	Warning      bool     `json:"has_warning"`
	Descriptions []string `json:"descriptions"`
	PublicError  string   `json:"public_error"`
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
	SetupType           string `json:"setup_type,omitempty"`
}

type DeleteDomainsRequest struct {
	DomainIDs []string `json:"domain_ids"`
}

type ModifyPHPVersionRequest struct {
	EnvironmentID                  string `json:"environment_id"`
	PHPVersion                     string `json:"php_version"`
	IsOptOutFromAutomaticPHPUpdate bool   `json:"is_opt_out_from_automatic_php_update"`
}

type ClearCacheRequest struct {
	EnvironmentID string `json:"environment_id"`
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

func (c *Client) ListActivityLogs(ctx context.Context, companyID, siteID, category string, limit int) ([]ActivityLog, error) {
	companyID = strings.TrimSpace(companyID)
	if companyID == "" {
		return nil, Error{Msg: "Kinsta company id is required to list activity logs"}
	}
	if limit <= 0 {
		limit = 10
	}
	values := url.Values{}
	values.Set("limit", strconv.Itoa(limit))
	if strings.TrimSpace(siteID) != "" {
		values.Set("site_id", strings.TrimSpace(siteID))
	}
	if strings.TrimSpace(category) != "" {
		values.Set("category", strings.TrimSpace(category))
	}
	var out activityLogsResponse
	if err := c.do(ctx, http.MethodGet, "/company/"+url.PathEscape(companyID)+"/activity-logs?"+values.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Logs(), nil
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

func (c *Client) DeleteDomains(ctx context.Context, envID string, domainIDs []string) (string, error) {
	var out operationResponse
	if err := c.do(ctx, http.MethodDelete, "/sites/environments/"+url.PathEscape(envID)+"/domains", DeleteDomainsRequest{DomainIDs: domainIDs}, &out); err != nil {
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

func (c *Client) ValidateDomainVerification(ctx context.Context, domainID string) (DomainVerificationValidation, error) {
	var out validateDomainVerificationResponse
	err := c.doGraphQL(ctx, "ValidateVerificationRecordsOfSiteDomains", `query ValidateVerificationRecordsOfSiteDomains($idSiteDomains: [String!]!) {
  validateVerificationRecordsOfSiteDomains(idSiteDomains: $idSiteDomains) {
    idSiteDomain
    isValid
    records {
      name
      value
      type
      isDetected
    }
  }
}`, map[string]any{"idSiteDomains": []string{domainID}}, &out)
	if err != nil {
		return DomainVerificationValidation{}, err
	}
	return out.Validation(domainID), nil
}

func (c *Client) ConfirmCloudflareVerification(ctx context.Context, envID, domainID string) (int, error) {
	var out confirmCloudflareVerificationResponse
	err := c.doGraphQL(ctx, "ConfirmCloudflareVerification", `mutation ConfirmCloudflareVerification($idEnvironment: String!, $idSiteDomain: String!, $isConfirmed: Boolean, $isHideNotification: Boolean) {
  idAction: initiateCloudflareVerification(
    idEnvironment: $idEnvironment
    idSiteDomain: $idSiteDomain
    isConfirmed: $isConfirmed
    isHideNotification: $isHideNotification
    runActionInBackground: true
  )
}`, map[string]any{"idEnvironment": envID, "idSiteDomain": domainID, "isConfirmed": true}, &out)
	if err != nil {
		return 0, err
	}
	return out.IDAction, nil
}

func (c *Client) GraphQLAction(ctx context.Context, actionID int) (GraphQLAction, error) {
	var out actionResponse
	err := c.doGraphQL(ctx, "Action", `query Action($idAction: Int!) {
  action(id: $idAction) {
    id
    error
    isDone
  }
  action_liveKeys(id: $idAction)
}`, map[string]any{"idAction": actionID}, &out)
	if err != nil {
		return GraphQLAction{}, err
	}
	return out.ActionStatus(actionID), nil
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

func (c *Client) ClearSiteCache(ctx context.Context, environmentID string) (string, error) {
	var out operationResponse
	if err := c.do(ctx, http.MethodPost, "/sites/tools/clear-cache", ClearCacheRequest{EnvironmentID: environmentID}, &out); err != nil {
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
	last := Operation{ID: operationID}
	for {
		op, err := c.Operation(ctx, operationID)
		if err != nil {
			return err
		}
		last = op
		status := strings.ToLower(strings.TrimSpace(op.Status))
		switch status {
		case "", "202", "queued", "pending", "processing", "running", "in_progress", "is_running":
		case "200", "complete", "completed", "success", "succeeded", "finished", "done", "has_completed":
			return nil
		case "failed", "failure", "error", "cancelled", "canceled", "has_failed":
			return Error{Msg: fmt.Sprintf("Kinsta operation %s failed: %s", operationID, operationStatusSummary(op))}
		default:
			// Unknown non-empty statuses are treated as still running. Kinsta has changed
			// operation labels before; this keeps polling conservative.
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Error{Msg: fmt.Sprintf("Timed out waiting for Kinsta operation %s: %v; last status: %s", operationID, ctx.Err(), operationStatusSummary(last))}
		case <-timer.C:
		}
	}
}

func operationStatusSummary(op Operation) string {
	status := strings.TrimSpace(op.Status)
	message := strings.TrimSpace(op.Message)
	if status == "" && message == "" {
		return "unknown"
	}
	if message == "" {
		return fmt.Sprintf("status %q", status)
	}
	if status == "" {
		return message
	}
	return fmt.Sprintf("status %q (%s)", status, message)
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
		return Error{Msg: fmt.Sprintf("Kinsta %s %s returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(data))), StatusCode: resp.StatusCode}
	}
	if out == nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *Client) doGraphQL(ctx context.Context, operationName, query string, variables any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	payload := map[string]any{"operationName": operationName, "variables": variables, "query": query}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := c.graphqlURL
	if endpoint == "" {
		endpoint = "https://graphql-router.kinsta.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/?opname="+url.QueryEscape(operationName), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apollographql-client-name", "mk-client")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respData, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Error{Msg: fmt.Sprintf("Kinsta GraphQL %s returned %s: %s", operationName, resp.Status, strings.TrimSpace(string(respData))), StatusCode: resp.StatusCode}
	}
	var wrapper graphQLResponse
	if err := json.Unmarshal(respData, &wrapper); err != nil {
		return err
	}
	if len(wrapper.Errors) > 0 {
		return Error{Msg: fmt.Sprintf("Kinsta GraphQL %s failed: %s", operationName, wrapper.ErrorSummary())}
	}
	if out == nil || len(wrapper.Data) == 0 || string(wrapper.Data) == "null" {
		return nil
	}
	return json.Unmarshal(wrapper.Data, out)
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (r graphQLResponse) ErrorSummary() string {
	parts := make([]string, 0, len(r.Errors))
	for _, err := range r.Errors {
		if msg := strings.TrimSpace(err.Message); msg != "" {
			parts = append(parts, msg)
		}
	}
	if len(parts) == 0 {
		return "unknown error"
	}
	return strings.Join(parts, "; ")
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
	Message        any       `json:"message"`
}

func (r operationStatusResponse) Operation() Operation {
	if r.OperationValue.ID != "" || r.OperationValue.Status != "" {
		return r.OperationValue
	}
	return Operation{ID: r.ID, Status: rawString(r.Status), Message: rawString(r.Message)}
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

type activityLogsResponse struct {
	Company struct {
		ActivityLogs activityLogsPage `json:"activity_logs"`
	} `json:"company"`
	ActivityLogs activityLogsPage `json:"activity_logs"`
}

type activityLogsPage struct {
	Items []ActivityLog `json:"items"`
}

func (r activityLogsResponse) Logs() []ActivityLog {
	if len(r.Company.ActivityLogs.Items) > 0 {
		return r.Company.ActivityLogs.Items
	}
	return r.ActivityLogs.Items
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
	Username   string `json:"username"`
	SSHUser    string `json:"ssh_user"`
	SFTPUser   string `json:"sftp_user"`
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
	cfg := SFTPConfig{Host: strings.TrimSpace(r.Host), Port: strings.TrimSpace(r.Port), User: firstTrimmed(r.User, r.Username, r.SSHUser, r.SFTPUser), Name: strings.TrimSpace(r.Name), SSHCommand: strings.TrimSpace(r.SSHCommand)}
	if cfg.Host == "" {
		cfg.Host = strings.TrimSpace(r.Site.Environment.ActiveContainer.LoadBalancer.ExtIP)
	}
	if cfg.Port == "" {
		cfg.Port = strings.TrimSpace(r.Site.Environment.ActiveContainer.LXDSshPort)
	}
	if cfg.User == "" {
		cfg.User = firstTrimmed(r.Site.User, sftpUserFromSSHCommand(cfg.SSHCommand))
	}
	if cfg.Name == "" {
		cfg.Name = strings.TrimSpace(r.Site.Name)
	}
	return cfg
}

func firstTrimmed(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func sftpUserFromSSHCommand(command string) string {
	for _, field := range strings.Fields(command) {
		if !strings.Contains(field, "@") {
			continue
		}
		parts := strings.SplitN(field, "@", 2)
		user := strings.Trim(parts[0], "'\"")
		if user != "" && user != "ssh" {
			return user
		}
	}
	return ""
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

type validateDomainVerificationResponse struct {
	ValidateVerificationRecordsOfSiteDomains []struct {
		IDSiteDomain string `json:"idSiteDomain"`
		IsValid      bool   `json:"isValid"`
		Records      []struct {
			Name       string `json:"name"`
			Value      string `json:"value"`
			Type       string `json:"type"`
			IsDetected bool   `json:"isDetected"`
		} `json:"records"`
	} `json:"validateVerificationRecordsOfSiteDomains"`
}

func (r validateDomainVerificationResponse) Validation(domainID string) DomainVerificationValidation {
	domainID = strings.TrimSpace(domainID)
	for _, item := range r.ValidateVerificationRecordsOfSiteDomains {
		if domainID != "" && strings.TrimSpace(item.IDSiteDomain) != domainID {
			continue
		}
		out := DomainVerificationValidation{DomainID: strings.TrimSpace(item.IDSiteDomain), Valid: item.IsValid, Records: make([]DomainVerificationRecord, 0, len(item.Records))}
		for _, record := range item.Records {
			out.Records = append(out.Records, DomainVerificationRecord{Name: strings.TrimSpace(record.Name), Value: strings.TrimSpace(record.Value), Type: strings.TrimSpace(record.Type), Detected: record.IsDetected})
		}
		return out
	}
	return DomainVerificationValidation{DomainID: domainID}
}

type confirmCloudflareVerificationResponse struct {
	IDAction int `json:"idAction"`
}

type actionResponse struct {
	Action *struct {
		ID     int    `json:"id"`
		Error  string `json:"error"`
		IsDone bool   `json:"isDone"`
	} `json:"action"`
}

func (r actionResponse) ActionStatus(actionID int) GraphQLAction {
	if r.Action == nil {
		return GraphQLAction{ID: actionID}
	}
	return GraphQLAction{ID: r.Action.ID, Found: true, Done: r.Action.IsDone, Error: strings.TrimSpace(r.Action.Error)}
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
