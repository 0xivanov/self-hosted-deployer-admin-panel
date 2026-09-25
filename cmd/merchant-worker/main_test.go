package main

import "testing"

func TestResolveMerchantWorkerSettings(t *testing.T) {
	for _, tt := range []struct {
		name, mode, legacy, config, wantMode, wantConfig string
		wantErr                                          bool
	}{
		{name: "legacy defaults test", legacy: "merchant.json", wantMode: "test", wantConfig: "merchant.json"},
		{name: "generic defaults test", config: "merchant.json", wantMode: "test", wantConfig: "merchant.json"},
		{name: "explicit live with legacy alias", mode: "live", legacy: "merchant.json", wantMode: "live", wantConfig: "merchant.json"},
		{name: "explicit live generic", mode: "live", config: "merchant.json", wantMode: "live", wantConfig: "merchant.json"},
		{name: "conflicting files", legacy: "a.json", config: "b.json", wantErr: true},
		{name: "live missing config", mode: "live", wantErr: true},
		{name: "invalid mode", mode: "sandbox", config: "merchant.json", wantErr: true},
		{name: "missing config", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mode, config, err := resolveMerchantWorkerSettings(tt.mode, tt.legacy, tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v", err)
			}
			if !tt.wantErr && (mode != tt.wantMode || config != tt.wantConfig) {
				t.Fatalf("got %q %q", mode, config)
			}
		})
	}
}

func TestMerchantWorkerFileMode(t *testing.T) {
	for _, tt := range []struct {
		name, flag, file, want string
		bad                    bool
	}{
		{"default", "", "", "test", false}, {"filelive", "", "live", "live", false}, {"flaglive", "live", "", "live", false}, {"matching", "live", "live", "live", false}, {"conflict", "test", "live", "", true}, {"unknown", "", "invalid", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMerchantWorkerMode(tt.flag, tt.file)
			if (err != nil) != tt.bad || got != tt.want {
				t.Fatalf("mode %q err %v", got, err)
			}
		})
	}
}
