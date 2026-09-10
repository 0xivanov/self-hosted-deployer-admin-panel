package nodelaunch

import "testing"

func TestUsageMatchesEveryUIDField(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		used       bool
	}{
		{"real", "Uid:\t60000 0 0 0\n", true}, {"effective", "Uid:\t0 60000 0 0\n", true},
		{"saved", "Uid:\t0 0 60000 0\n", true}, {"filesystem", "Uid:\t0 0 0 60000\n", true},
		{"other", "Name:\tfixture\nUid:\t60001 60001 60001 60001\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used, err := statusUsesUID([]byte(tc.body), 60000)
			if err != nil || used != tc.used {
				t.Fatal(used, err)
			}
		})
	}
	for _, tc := range []struct{ name, body string }{
		{"missing_uid", ""}, {"short_uid", "Uid: 60000\n"}, {"negative_uid", "Uid: -1 0 0 0\n"}, {"duplicate_uid", "Uid: 0 0 0 0\nUid: 60000 60000 60000 60000\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := statusUsesUID([]byte(tc.body), 60000); err == nil {
				t.Fatal("incomplete status accepted")
			}
		})
	}
}
func TestUsageCgroupRequiresPopulatedFact(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body     string
		present, valid bool
	}{
		{"empty", "populated 0\nfrozen 0\n", false, true}, {"descendants", "populated 1\nfrozen 0\n", true, true},
		{"missing", "frozen 0\n", false, false}, {"duplicate", "populated 0\npopulated 1\n", false, false}, {"invalid", "populated 2\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			present, err := populatedCgroup([]byte(tc.body))
			if (err == nil) != tc.valid || err == nil && present != tc.present {
				t.Fatal(present, err)
			}
		})
	}
}
func TestUsageFindsTCPListenersAcrossAddressFamilies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, address, state string
		digits               int
		present              bool
	}{
		{"loopback", "0100007F:7C85", "0A", 8, true}, {"wildcard", "00000000:7C85", "0A", 8, true},
		{"ipv6", "00000000000000000000000000000000:7C85", "0A", 32, true},
		{"other_port", "0100007F:7C86", "0A", 8, false}, {"established", "0100007F:7C85", "01", 8, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := "sl local_address rem_address st\n0: " + tc.address + " 00000000:0000 " + tc.state + "\n"
			present, err := hasTCPListener([]byte(data), 31877, tc.digits)
			if err != nil || present != tc.present {
				t.Fatal(present, err)
			}
		})
	}
	for _, tc := range []struct{ name, data string }{
		{"missing_header", ""}, {"bad_address", "sl local_address rem_address st\n0: bad 0 0A\n"}, {"bad_port", "sl local_address rem_address st\n0: 00000000:XXXX 0 0A\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := hasTCPListener([]byte(tc.data), 31877, 8); err == nil {
				t.Fatal("invalid TCP table accepted")
			}
		})
	}
}
