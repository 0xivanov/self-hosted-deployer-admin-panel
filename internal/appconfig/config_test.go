package appconfig

import "testing"

func TestParsePreservesNestedFields(t *testing.T) {
	c, e := Parse([]byte("name: hello\nimage: nginx\ndeploy:\n  replicas: 2\nrouting:\n  domain: demo.example\nstate:\n  mode: stateful\nhosting:\n  version: v1\n"))
	if e != nil {
		t.Fatal(e)
	}
	if c.Replicas() != 2 || c.Domain() != "demo.example" || c.StateMode() != "stateful" || c["hosting"] == nil {
		t.Fatalf("lost nested config: %#v", c)
	}
}
