package kuma

import (
	"reflect"
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promauth"
)

func Test_buildAPIPath(t *testing.T) {
	apiConf, err := newAPIConfig(&SDConfig{
		Server:            "http://localhost:5676",
		HTTPClientConfig:  promauth.HTTPClientConfig{},
		ProxyClientConfig: promauth.ProxyClientConfig{},
	}, ".")

	if err != nil {
		t.Errorf("buildAPIPath() error = %v", err)
		return
	}

	const wantedPath = "/v3/discovery:monitoringassignments"
	if apiConf.path != wantedPath {
		t.Errorf("buildAPIPath() got = %v, want = %v", apiConf.path, wantedPath)
	}
}

func Test_parseAPIResponse(t *testing.T) {
	type args struct {
		data []byte
		path string
	}
	tests := []struct {
		name    string
		args    args
		want    []kumaTarget
		wantErr bool
	}{

		{
			name: "parse ok",
			args: args{
				data: []byte(`{
    "version_info":"5dc9a5dd-2091-4426-a886-dfdc24fc99d7",
    "resources":[
       {
          "@type":"type.googleapis.com/kuma.observability.v1.MonitoringAssignment",
          "mesh":"default",
          "service":"redis",
          "targets":[
             {
                "name":"redis",
                "scheme":"http",
                "address":"127.0.0.1:5670",
                "metrics_path":"/metrics",
                "labels":{ "kuma_io_protocol":"tcp" }
             }
          ]
       },
       {
          "@type":"type.googleapis.com/kuma.observability.v1.MonitoringAssignment",
          "mesh":"default",
          "service":"app",
          "targets":[
             {
                "name":"app",
                "scheme":"http",
                "address":"127.0.0.1:5671",
                "metrics_path":"/metrics",
                "labels":{ "kuma_io_protocol":"http" }
             }
          ]
       }
    ],
    "type_url":"type.googleapis.com/kuma.observability.v1.MonitoringAssignment"
 }`),
			},
			want: []kumaTarget{
				{
					Mesh:        "default",
					Service:     "redis",
					DataPlane:   "redis",
					Instance:    "redis",
					Scheme:      "http",
					Address:     "127.0.0.1:5670",
					MetricsPath: "/metrics",
					Labels:      map[string]string{"kuma_io_protocol": "tcp"},
				},
				{
					Mesh:        "default",
					Service:     "app",
					DataPlane:   "app",
					Instance:    "app",
					Scheme:      "http",
					Address:     "127.0.0.1:5671",
					MetricsPath: "/metrics",
					Labels:      map[string]string{"kuma_io_protocol": "http"},
				},
			},
		},

		{
			name: "api version err",
			args: args{
				data: []byte(`{
    "resources":[
       {
          "@type":"type.googleapis.com/kuma.observability.v2.MonitoringAssignment",
          "mesh":"default",
          "service":"redis",
          "targets":[]
       }
    ],
    "type_url":"type.googleapis.com/kuma.observability.v2.MonitoringAssignment"
 }`),
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAPIResponse(tt.args.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseAPIResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseAPIResponse() got = %v, want %v", got, tt.want)
			}
		})
	}
}
