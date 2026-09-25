package portal

import "errors"

var ErrMerchantProviderMode = errors.New("merchant provider must use test billing mode")

type merchantBillingModeProvider interface {
	BillingMode() string
}

// validateMerchantProviderMode keeps the merchant tables test-only while the
// merchant schema has no live-mode partition. Existing test doubles may omit
// BillingMode; explicit live or unknown modes are rejected before any state or
// provider operation.
func validateMerchantProviderMode(provider any) error {
	if provider == nil {
		return ErrInvalid
	}
	if modeProvider, ok := provider.(merchantBillingModeProvider); ok && modeProvider.BillingMode() != "test" {
		return ErrMerchantProviderMode
	}
	return nil
}
