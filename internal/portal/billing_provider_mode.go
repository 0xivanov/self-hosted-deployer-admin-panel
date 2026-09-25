package portal

// billingModeProvider is implemented by real provider clients. Test doubles
// may omit it while the store remains in its default test mode.
type billingModeProvider interface {
	BillingMode() string
}

func (s *Store) validateBillingProviderMode(provider any) error {
	if provider == nil {
		return ErrInvalid
	}
	selected := s.billingModeValue()
	modeProvider, ok := provider.(billingModeProvider)
	if !ok {
		if selected == "test" {
			return nil
		}
		return ErrBillingConflict
	}
	if modeProvider.BillingMode() != selected {
		return ErrBillingConflict
	}
	return nil
}
