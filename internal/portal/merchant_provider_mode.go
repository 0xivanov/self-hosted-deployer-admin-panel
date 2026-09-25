package portal

import "errors"

var ErrMerchantProviderMode = errors.New("merchant provider does not match selected payment mode")

type merchantBillingModeProvider interface {
	BillingMode() string
}

// validateMerchantProviderMode requires a real provider to match the
// operator-selected merchant partition. Test doubles may omit BillingMode only
// when the selected partition is test.
func (s *Store) validateMerchantProviderMode(provider any) error {
	if provider == nil {
		return ErrInvalid
	}
	modeProvider, ok := provider.(merchantBillingModeProvider)
	if !ok {
		if s.merchantModeValue() == "test" {
			return nil
		}
		return ErrMerchantProviderMode
	}
	if modeProvider.BillingMode() != s.merchantModeValue() {
		return ErrMerchantProviderMode
	}
	return nil
}
