package portal

import (
	"context"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

type modeOnlyProvider struct{ mode string }

func (p modeOnlyProvider) BillingMode() string { return p.mode }

func TestValidateBillingProviderMode(t *testing.T) {
	tests := []struct {
		name     string
		store    string
		provider any
		wantErr  bool
	}{
		{name: "matching test", store: "test", provider: modeOnlyProvider{mode: "test"}},
		{name: "matching live", store: "live", provider: modeOnlyProvider{mode: "live"}},
		{name: "mismatch", store: "live", provider: modeOnlyProvider{mode: "test"}, wantErr: true},
		{name: "legacy fake remains test compatible", store: "test", provider: struct{}{}},
		{name: "legacy fake denied in live", store: "live", provider: struct{}{}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{billingMode: tt.store}
			if err := s.validateBillingProviderMode(tt.provider); (err != nil) != tt.wantErr {
				t.Fatalf("validateBillingProviderMode() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

type mismatchedChargeReader struct{ modeOnlyProvider }

func (mismatchedChargeReader) RetrieveChargeObservation(context.Context, string) (hostingbilling.ChargeObservation, error) {
	panic("provider must be rejected before network call")
}

func TestLiveChargeReconciliationRejectsMismatchedProviderBeforeDB(t *testing.T) {
	s := &Store{billingMode: "live"}
	p := mismatchedChargeReader{modeOnlyProvider{mode: "test"}}
	if err := s.ReconcileBillingCharge(context.Background(), p, "ch_example"); err != ErrBillingConflict {
		t.Fatalf("ReconcileBillingCharge() error = %v, want %v", err, ErrBillingConflict)
	}
}
