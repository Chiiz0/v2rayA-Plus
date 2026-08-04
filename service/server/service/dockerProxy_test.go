package service

import (
	"testing"

	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

func TestSelectDockerProxyCore(t *testing.T) {
	tests := []struct {
		name            string
		core            configure.DockerProxyCore
		detectedVariant where.Variant
		detectedVersion string
		wantRequested   configure.DockerProxyCore
		wantCore        configure.DockerProxyCore
		wantVariant     where.Variant
		wantVersion     string
		wantImage       string
		wantMount       string
		wantErr         bool
	}{
		{
			name:            "legacy empty follows v2fly",
			detectedVariant: where.V2ray,
			detectedVersion: "5.41.0",
			wantRequested:   configure.DockerProxyCoreAuto,
			wantCore:        configure.DockerProxyCoreV2Fly,
			wantVariant:     where.V2ray,
			wantVersion:     "5.41.0",
			wantImage:       "v2fly/v2fly-core:v5.41.0",
			wantMount:       "/etc/v2ray",
		},
		{
			name:            "auto follows xray",
			core:            configure.DockerProxyCoreAuto,
			detectedVariant: where.Xray,
			detectedVersion: "26.7.28",
			wantRequested:   configure.DockerProxyCoreAuto,
			wantCore:        configure.DockerProxyCoreXray,
			wantVariant:     where.Xray,
			wantVersion:     "26.7.28",
			wantImage:       "teddysun/xray:latest",
			wantMount:       "/etc/xray",
		},
		{
			name:            "explicit xray overrides v2fly",
			core:            configure.DockerProxyCoreXray,
			detectedVariant: where.V2ray,
			detectedVersion: "5.41.0",
			wantRequested:   configure.DockerProxyCoreXray,
			wantCore:        configure.DockerProxyCoreXray,
			wantVariant:     where.Xray,
			wantImage:       "teddysun/xray:latest",
			wantMount:       "/etc/xray",
		},
		{
			name:            "explicit v2fly overrides xray",
			core:            configure.DockerProxyCoreV2Fly,
			detectedVariant: where.Xray,
			detectedVersion: "26.7.28",
			wantRequested:   configure.DockerProxyCoreV2Fly,
			wantCore:        configure.DockerProxyCoreV2Fly,
			wantVariant:     where.V2ray,
			wantImage:       "v2fly/v2fly-core:latest",
			wantMount:       "/etc/v2ray",
		},
		{name: "invalid", core: "sing-box", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectDockerProxyCore(tt.core, tt.detectedVariant, tt.detectedVersion)
			if (err != nil) != tt.wantErr {
				t.Fatalf("selectDockerProxyCore() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.RequestedCore != tt.wantRequested || got.Core != tt.wantCore || got.Variant != tt.wantVariant ||
				got.CoreVersion != tt.wantVersion || got.ImageName != tt.wantImage || got.MountPath != tt.wantMount {
				t.Fatalf("selectDockerProxyCore() = %+v", got)
			}
		})
	}
}
