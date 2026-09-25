package portal

import (
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
	"testing"
)

type merchantModeFixture string

func (m merchantModeFixture) BillingMode() string { return string(m) }

func TestValidateMerchantProviderMode(t *testing.T) {
	for name, provider := range map[string]any{
		"legacy test double": struct{}{},
		"explicit test":      merchantModeFixture("test"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateMerchantProviderMode(provider); err != nil {
				t.Fatalf("provider rejected: %v", err)
			}
		})
	}
	for _, mode := range []string{"live", "production", ""} {
		if err := validateMerchantProviderMode(merchantModeFixture(mode)); !errors.Is(err, ErrMerchantProviderMode) {
			t.Fatalf("mode %q error=%v, want ErrMerchantProviderMode", mode, err)
		}
	}
	if err := validateMerchantProviderMode(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil error=%v, want ErrInvalid", err)
	}
}

type liveMerchantAccountProvider struct{ MerchantAccountProvider }

func (liveMerchantAccountProvider) BillingMode() string { return "live" }

type liveMerchantCheckoutProvider struct{ MerchantCheckoutProvider }

func (liveMerchantCheckoutProvider) BillingMode() string { return "live" }

func TestMerchantLiveProviderRejectedBeforeDatabaseOrNetwork(t *testing.T) {
	s := &Store{billingMode: "live"} // Hosting mode must not enable merchant sales.
	if _, err := s.DispatchMerchantAccount(t.Context(), "request", liveMerchantAccountProvider{}); !errors.Is(err, ErrMerchantProviderMode) {
		t.Fatal(err)
	}
	if _, err := s.DispatchMerchantOrder(t.Context(), "order", liveMerchantCheckoutProvider{}); !errors.Is(err, ErrMerchantProviderMode) {
		t.Fatal(err)
	}
	if _, _, err := s.ProcessMerchantEvents(t.Context(), liveMerchantCheckoutProvider{}, 1); !errors.Is(err, ErrMerchantProviderMode) {
		t.Fatal(err)
	}
	if err := s.AcceptMerchantEvent(t.Context(), merchantbilling.CheckoutEvent{Live: true, ID: "evt_live"}); !errors.Is(err, ErrMerchantProviderMode) {
		t.Fatal(err)
	}
}
