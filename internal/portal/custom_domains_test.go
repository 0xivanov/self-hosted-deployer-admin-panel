package portal

import "testing"

func TestValidCustomHostname(t *testing.T) {
	tests := []struct {
		name, input string
		valid       bool
	}{
		{"normalizes", " Example.COM. ", true}, {"ip", "192.0.2.1", false}, {"wildcard", "*.example.com", false},
		{"provider", "1.2.3.4.sslip.io", false}, {"infra apex", "0xivanov.dev", false}, {"other subdomain", "customer.0xivanov.dev", true},
		{"localhost", "localhost", false}, {"private suffix", "foo.internal", false}, {"bad label", "-foo.example.com", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validCustomHostname(tc.input)
			if (err == nil) != tc.valid {
				t.Fatalf("validCustomHostname(%q) error=%v", tc.input, err)
			}
			if tc.valid && got == "" {
				t.Fatal("empty normalized hostname")
			}
		})
	}
}
