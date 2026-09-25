package main

import "testing"

func TestBillingWorkerMode(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		ok          bool
	}{
		{"", "test", true},
		{"test", "test", true},
		{"live", "live", true},
		{"staging", "", false},
	} {
		got, err := billingWorkerMode(tc.input)
		if tc.ok && (err != nil || got != tc.want) {
			t.Fatalf("mode %q: got %q, %v", tc.input, got, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("mode %q accepted", tc.input)
		}
	}
}
