package main

import "testing"

func TestClientEndpointIsPrivate(t *testing.T) {
	for _, value := range []string{"http://example.com:8787", "http://0.0.0.0:8787", "https://127.0.0.1", "http://user:pass@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1?token=x"} {
		t.Setenv("WLHL_URL", value)
		if _, err := endpoint(); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	t.Setenv("WLHL_URL", "http://127.0.0.1:8787")
	if _, err := endpoint(); err != nil {
		t.Fatal(err)
	}
}
