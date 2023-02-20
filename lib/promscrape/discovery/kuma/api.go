package kuma

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/logger"
	"io"
	"net/http"
	"net/url"
	"path"
	"sync"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promscrape/discoveryutils"
	"github.com/VictoriaMetrics/metrics"
)

var configMap = discoveryutils.NewConfigMap()

type apiConfig struct {
	client *discoveryutils.Client
	path   string

	cancel        context.CancelFunc
	targetsMutex  sync.RWMutex
	targets       []kumaTarget
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

func getAPIConfig(sdc *SDConfig, baseDir string) (*apiConfig, error) {
	v, err := configMap.Get(sdc, func() (interface{}, error) { return newAPIConfig(sdc, baseDir) })
	if err != nil {
		return nil, err
	}
	return v.(*apiConfig), nil
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

	// start updating targets with a long polling in background
	cancel, initCh := initAndRepeatWithInterval(*SDCheckInterval, func(ctx context.Context) {
		// we are constantly waiting for targets updates in long polling requests
		err := cfg.updateTargets(ctx)
		if err != nil {
			logger.Errorf("there were errors when discovering kuma targets, so preserving the previous targets. error: %v", err)
		}
	})
	cfg.cancel = cancel
	// wait for initial targets update
	<-initCh

	return cfg, nil
}

func (cfg *apiConfig) getTargets() ([]kumaTarget, error) {
	cfg.targetsMutex.RLock()
	defer cfg.targetsMutex.RUnlock()
	return cfg.targets, nil
}

func (cfg *apiConfig) updateTargets(ctx context.Context) error {
	requestBody, err := json.Marshal(discoveryRequest{
		VersionInfo:   cfg.latestVersion,
		Node:          discoveryRequestNode{Id: discoveryNode},
		TypeUrl:       xdsResourceTypeUrl,
		ResponseNonce: cfg.latestNonce,
	})
	if err != nil {
		return fmt.Errorf("cannot marshal request body for kuma_sd api: %w", err)
	}

	var statusCode int
	data, err := cfg.client.GetBlockingAPIResponseWithParamsCtx(
		ctx,
		cfg.path,
		func(request *http.Request) {
			request.Method = http.MethodPost
			request.Body = io.NopCloser(bytes.NewReader(requestBody))

			// set max duration for long polling request
			query := request.URL.Query()
			query.Add("fetch-timeout", cfg.getWaitTime().String())
			request.URL.RawQuery = query.Encode()

			request.Header.Set("Accept", "application/json")
			request.Header.Set("Content-Type", "application/json")
		},
		func(response *http.Response) {
			statusCode = response.StatusCode
		},
	)
	if statusCode == http.StatusNotModified {
		return nil
	}
	if err != nil {
		cfg.fetchErrors.Inc()
		return fmt.Errorf("cannot read kuma_sd api response: %w", err)
	}

	response, err := parseDiscoveryResponse(data)
	if err != nil {
		cfg.parseErrors.Inc()
		return fmt.Errorf("cannot parse kuma_sd api response: %w", err)
	}

	cfg.targetsMutex.Lock()
	defer cfg.targetsMutex.Unlock()
	cfg.targets = parseKumaTargets(response)
	cfg.latestVersion = response.VersionInfo
	cfg.latestNonce = response.Nonce

	return nil
}

func (cfg *apiConfig) getWaitTime() time.Duration {
	return discoveryutils.BlockingClientReadTimeout - discoveryutils.BlockingClientReadTimeout/8
}

func (cfg *apiConfig) mustStop() {
	cfg.cancel()
	cfg.client.Stop()
}
