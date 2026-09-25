package main

import "errors"

// resolveBillingSettings preserves the old test flags without letting them
// silently select live billing. It runs before opening the database.
func resolveBillingSettings(mode string, legacyTest bool, legacyWebhook, legacyManagement, webhook, management string, development bool) (string, string, string, error) {
	invalid := func() (string, string, string, error) {
		return "", "", "", errors.New("conflicting or incomplete billing settings")
	}
	if mode != "" && mode != "test" && mode != "live" {
		return invalid()
	}
	if legacyTest {
		if mode == "live" {
			return invalid()
		}
		mode = "test"
	}
	if legacyWebhook != "" || legacyManagement != "" {
		if mode != "test" {
			return invalid()
		}
	}
	if legacyWebhook != "" {
		if webhook != "" {
			return invalid()
		}
		webhook = legacyWebhook
	}
	if legacyManagement != "" {
		if management != "" {
			return invalid()
		}
		management = legacyManagement
	}
	if (webhook != "" || management != "") && (mode == "" || development) {
		return invalid()
	}
	if mode == "live" && (development || webhook == "" || management == "") {
		return invalid()
	}
	return mode, webhook, management, nil
}

// resolveMerchantSettings selects the merchant partition before the portal DB
// is opened. Legacy test flags remain aliases for the explicit test mode.
func resolveMerchantSettings(mode string, legacyConfig, legacyWebhook, config, webhook string, development bool) (string, string, string, error) {
	invalid := func() (string, string, string, error) {
		return "", "", "", errors.New("conflicting or incomplete merchant settings")
	}
	if mode != "" && mode != "test" && mode != "live" {
		return invalid()
	}
	if legacyConfig != "" || legacyWebhook != "" {
		if mode == "live" || config != "" && config != legacyConfig || webhook != "" && webhook != legacyWebhook {
			return invalid()
		}
		mode = "test"
		if config == "" {
			config = legacyConfig
		}
		if webhook == "" {
			webhook = legacyWebhook
		}
	}
	if mode == "" {
		mode = "test"
	}
	if development && mode == "live" {
		return invalid()
	}
	if mode == "live" && (config == "" || webhook == "") {
		return invalid()
	}
	if webhook != "" && config == "" {
		return invalid()
	}
	return mode, config, webhook, nil
}
