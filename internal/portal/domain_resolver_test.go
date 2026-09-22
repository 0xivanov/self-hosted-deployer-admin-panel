package portal

import "testing"

func TestCustomDomainDNSAddressValidation(t *testing.T) {
	for _, address := range []string{"", "dns.example:53", "1.1.1.1", "1.1.1.1:0", "0.0.0.0:53", "224.0.0.1:53"} {
		if _, err := CustomDomainDNS(address); err == nil {
			t.Fatalf("accepted %q", address)
		}
	}
	for _, address := range []string{"1.1.1.1:53", "[2606:4700:4700::1111]:53"} {
		if _, err := CustomDomainDNS(address); err != nil {
			t.Fatal(err)
		}
	}
}
