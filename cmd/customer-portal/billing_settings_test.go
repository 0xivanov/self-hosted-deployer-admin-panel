package main

import "testing"

func TestResolveBillingSettings(t *testing.T) {
	cases := []struct {
		name, mode                                     string
		legacy                                         bool
		oldWebhook, oldManagement, webhook, management string
		demo                                           bool
		want                                           string
		bad                                            bool
	}{
		{name: "disabled"},
		{name: "existing test configuration", legacy: true, oldWebhook: "test-hook", oldManagement: "test-management", want: "test"},
		{name: "explicit test", mode: "test", webhook: "test-hook", management: "test-management", want: "test"},
		{name: "explicit live", mode: "live", webhook: "live-hook", management: "live-management", want: "live"},
		{name: "unknown", mode: "production", bad: true},
		{name: "mixed flags", mode: "live", legacy: true, webhook: "live-hook", management: "live-management", bad: true},
		{name: "old secret in live", mode: "live", oldWebhook: "test-hook", management: "live-management", bad: true},
		{name: "ambiguous webhook", legacy: true, oldWebhook: "test-hook", webhook: "another", bad: true},
		{name: "ambiguous management", legacy: true, oldManagement: "test-management", management: "another", bad: true},
		{name: "disabled with webhook", webhook: "hook", bad: true},
		{name: "live missing webhook", mode: "live", management: "management", bad: true},
		{name: "live missing management", mode: "live", webhook: "hook", bad: true},
		{name: "live demo", mode: "live", webhook: "hook", management: "management", demo: true, bad: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, hook, management, err := resolveBillingSettings(c.mode, c.legacy, c.oldWebhook, c.oldManagement, c.webhook, c.management, c.demo)
			if (err != nil) != c.bad {
				t.Fatalf("error=%v expected failure=%v", err, c.bad)
			}
			if c.bad {
				return
			}
			if mode != c.want {
				t.Fatalf("mode=%q want=%q", mode, c.want)
			}
			wantHook := c.webhook
			if c.oldWebhook != "" {
				wantHook = c.oldWebhook
			}
			wantManagement := c.management
			if c.oldManagement != "" {
				wantManagement = c.oldManagement
			}
			if hook != wantHook || management != wantManagement {
				t.Fatal("configuration paths changed")
			}
		})
	}
}

func TestResolveMerchantSettings(t *testing.T) {
	tests := []struct {
		name, mode, legacyConfig, legacyWebhook, config, webhook string
		development                                              bool
		wantMode, wantConfig, wantWebhook                        string
		wantErr                                                  bool
	}{
		{name: "defaults to test", wantMode: "test"},
		{name: "live requires both", mode: "live", config: "merchant.json", webhook: "secret", wantMode: "live", wantConfig: "merchant.json", wantWebhook: "secret"},
		{name: "legacy test aliases", legacyConfig: "test.json", legacyWebhook: "test.secret", wantMode: "test", wantConfig: "test.json", wantWebhook: "test.secret"},
		{name: "live missing webhook", mode: "live", config: "merchant.json", wantErr: true},
		{name: "live demo", mode: "live", config: "merchant.json", webhook: "secret", development: true, wantErr: true},
		{name: "legacy conflicts live", mode: "live", legacyConfig: "test.json", wantErr: true},
		{name: "webhook without config", webhook: "secret", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, config, webhook, err := resolveMerchantSettings(tt.mode, tt.legacyConfig, tt.legacyWebhook, tt.config, tt.webhook, tt.development)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v", err)
			}
			if !tt.wantErr && (mode != tt.wantMode || config != tt.wantConfig || webhook != tt.wantWebhook) {
				t.Fatalf("got %q %q %q", mode, config, webhook)
			}
		})
	}
}
