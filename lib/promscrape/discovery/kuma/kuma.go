package kuma

import (
	"flag"
	"fmt"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promauth"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutils"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/proxy"
)

// SDCheckInterval defines interval for targets refresh.
var SDCheckInterval = flag.Duration("promscrape.kumaSDCheckInterval", time.Minute, "Interval for checking for changes in kuma service discovery. "+
	"This works only if kuma_sd_configs is configured in '-promscrape.config' file. "+
	"See https://docs.victoriametrics.com/sd_configs.html#kuma_sd_configs for details")

// SDConfig represents service discovery config for Kuma Service Mesh.
//
// See https://prometheus.io/docs/prometheus/latest/configuration/configuration/#kuma_sd_config
type SDConfig struct {
	Server            string                     `yaml:"server"`
	HTTPClientConfig  promauth.HTTPClientConfig  `yaml:",inline"`
	ProxyURL          *proxy.URL                 `yaml:"proxy_url,omitempty"`
	ProxyClientConfig promauth.ProxyClientConfig `yaml:",inline"`
}

// GetLabels returns kuma service discovery labels according to sdc.
func (sdc *SDConfig) GetLabels(baseDir string) ([]*promutils.Labels, error) {
	cfg, err := getAPIConfig(sdc, baseDir)
	if err != nil {
		return nil, fmt.Errorf("cannot get API config for kuma_sd: %w", err)
	}
	targets, err := getKumaTargets(cfg)
	if err != nil {
		return nil, err
	}
	return kumaTargetsToLabels(targets, sdc.Server), nil
}

func kumaTargetsToLabels(src []kumaTarget, sourceURL string) []*promutils.Labels {
	ms := make([]*promutils.Labels, 0, len(src))
	for _, target := range src {
		m := promutils.NewLabels(8 + len(target.Labels))

		m.Add("instance", target.Instance)
		m.Add("__address__", target.Address)
		m.Add("__scheme__", target.Scheme)
		m.Add("__metrics_path__", target.MetricsPath)
		m.Add("__meta_server", sourceURL)
		m.Add("__meta_kuma_mesh", target.Mesh)
		m.Add("__meta_kuma_service", target.Service)
		m.Add("__meta_kuma_dataplane", target.DataPlane)
		for k, v := range target.Labels {
			m.Add("__meta_kuma_label_"+k, v)
		}

		m.RemoveDuplicates()
		ms = append(ms, m)
	}
	return ms
}

// MustStop stops further usage for sdc.
func (sdc *SDConfig) MustStop() {
	configMap.Delete(sdc)
}
