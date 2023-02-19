package kuma

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promscrape/discoveryutils"
	"github.com/VictoriaMetrics/metrics"
)

var configMap = discoveryutils.NewConfigMap()

type apiConfig struct {
	client        *discoveryutils.Client
	path          string
	latestVersion string
	latestNonce   string

	fetchErrors *metrics.Counter
	parseErrors *metrics.Counter
}

const (
	xdsApiVersion      = "v3"
	xdsRequestType     = "discovery"
	xdsResourceType    = "monitoringassignments"
	xdsResourceTypeUrl = "type.googleapis.com/kuma.observability.v1.MonitoringAssignment"
	discoveryNode      = "victoria-metrics"
)

type kumaTarget struct {
	Mesh         string            `json:"mesh"`
	ControlPlane string            `json:"controlplane"`
	Service      string            `json:"service"`
	DataPlane    string            `json:"dataplane"`
	Instance     string            `json:"instance"`
	Scheme       string            `json:"scheme"`
	Address      string            `json:"address"`
	MetricsPath  string            `json:"metrics_path"`
	Labels       map[string]string `json:"labels"`
}

// discoveryRequest represent xDS-requests for Kuma Service Mesh
// https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/discovery/v3/discovery.proto#envoy-v3-api-msg-service-discovery-v3-discoveryrequest
type discoveryRequest struct {
	VersionInfo   string               `json:"version_info,omitempty"`
	Node          discoveryRequestNode `json:"node,omitempty"`
	ResourceNames []string             `json:"resource_names,omitempty"`
	TypeUrl       string               `json:"type_url,omitempty"`
	ResponseNonce string               `json:"response_nonce,omitempty"`
}

type discoveryRequestNode struct {
	Id string `json:"id,omitempty"`
}

// discoveryResponse represent xDS-requests for Kuma Service Mesh
// https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/discovery/v3/discovery.proto#envoy-v3-api-msg-service-discovery-v3-discoveryresponse
type discoveryResponse struct {
	VersionInfo string `json:"version_info,omitempty"`
	Resources   []struct {
		Mesh    string `json:"mesh,omitempty"`
		Service string `json:"service,omitempty"`
		Targets []struct {
			Name        string            `json:"name,omitempty"`
			Scheme      string            `json:"scheme,omitempty"`
			Address     string            `json:"address,omitempty"`
			MetricsPath string            `json:"metrics_path,omitempty"`
			Labels      map[string]string `json:"labels,omitempty"`
		} `json:"targets,omitempty"`
		Labels map[string]string `json:"labels,omitempty"`
	} `json:"resources,omitempty"`
	TypeUrl      string `json:"type_url,omitempty"`
	Nonce        string `json:"nonce,omitempty"`
	ControlPlane struct {
		Identifier string `json:"identifier,omitempty"`
	} `json:"control_plane,omitempty"`
}

func newAPIConfig(sdc *SDConfig, baseDir string) (*apiConfig, error) {
	ac, err := sdc.HTTPClientConfig.NewConfig(baseDir)
	if err != nil {
		return nil, fmt.Errorf("cannot parse auth config: %w", err)
	}
	parsedURL, err := url.Parse(sdc.Server)
	if err != nil {
		return nil, fmt.Errorf("cannot parse kuma_sd server URL: %w", err)
	}
	apiServer := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)

	proxyAC, err := sdc.ProxyClientConfig.NewConfig(baseDir)
	if err != nil {
		return nil, fmt.Errorf("cannot parse proxy auth config: %w", err)
	}
	client, err := discoveryutils.NewClient(apiServer, ac, sdc.ProxyURL, proxyAC)
	if err != nil {
		return nil, fmt.Errorf("cannot create HTTP client for %q: %w", apiServer, err)
	}

	apiPath := path.Join(
		parsedURL.RequestURI(),
		xdsApiVersion,
		xdsRequestType+":"+xdsResourceType,
	)
	cfg := &apiConfig{
		client:      client,
		path:        apiPath,
		fetchErrors: metrics.GetOrCreateCounter(fmt.Sprintf(`promscrape_discovery_kuma_errors_total{type="fetch",url=%q}`, sdc.Server)),
		parseErrors: metrics.GetOrCreateCounter(fmt.Sprintf(`promscrape_discovery_kuma_errors_total{type="parse",url=%q}`, sdc.Server)),
	}
	return cfg, nil
}

func getAPIConfig(sdc *SDConfig, baseDir string) (*apiConfig, error) {
	v, err := configMap.Get(sdc, func() (interface{}, error) { return newAPIConfig(sdc, baseDir) })
	if err != nil {
		return nil, err
	}
	return v.(*apiConfig), nil
}

func getKumaTargets(cfg *apiConfig) ([]kumaTarget, error) {
	requestBody, err := json.Marshal(discoveryRequest{
		VersionInfo:   cfg.latestVersion,
		Node:          discoveryRequestNode{Id: discoveryNode},
		TypeUrl:       xdsResourceTypeUrl,
		ResponseNonce: cfg.latestNonce,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot marshal request body for kuma_sd api: %w", err)
	}

	data, err := cfg.client.GetAPIResponseWithReqParams(cfg.path, func(request *http.Request) {
		request.Method = http.MethodPost
		request.Body = io.NopCloser(bytes.NewReader(requestBody))
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/json")
	})

	if err != nil {
		cfg.fetchErrors.Inc()
		return nil, fmt.Errorf("cannot read kuma_sd api response: %w", err)
	}
	targets, err := parseAPIResponse(data)
	if err != nil {
		cfg.parseErrors.Inc()
		return nil, err
	}

	//cfg.latestVersion = response.VersionInfo
	//cfg.latestNonce = response.Nonce

	return targets, nil
}

func parseAPIResponse(data []byte) ([]kumaTarget, error) {
	response := discoveryResponse{}
	err := json.Unmarshal(data, &response)
	if err != nil {
		return nil, fmt.Errorf("cannot parse kuma_sd api response, err:  %w", err)
	}
	if response.TypeUrl != xdsResourceTypeUrl {
		return nil, fmt.Errorf("unexpected type_url in kuma_sd api response, expected: %s, got: %s", xdsResourceTypeUrl, response.TypeUrl)
	}

	result := make([]kumaTarget, 0, len(response.Resources))
	for _, resource := range response.Resources {
		for _, target := range resource.Targets {
			labels := make(map[string]string)
			for label, value := range resource.Labels {
				labels[label] = value
			}
			for label, value := range target.Labels {
				labels[label] = value
			}
			result = append(result, kumaTarget{
				Mesh:         resource.Mesh,
				ControlPlane: response.ControlPlane.Identifier,
				Service:      resource.Service,
				DataPlane:    target.Name,
				Instance:     target.Name,
				Scheme:       target.Scheme,
				Address:      target.Address,
				MetricsPath:  target.MetricsPath,
				Labels:       labels,
			})
		}
	}

	return result, nil
}
