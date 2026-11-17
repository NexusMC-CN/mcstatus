package config

import (
	"strings"
	"testing"
)

func TestOptionalAPITokenValidation(t *testing.T) {
	for _, token := range []string{"", strings.Repeat("a", 32), strings.Repeat("b", 512)} {
		t.Setenv("MCSTATUS_API_TOKEN", token)
		if Load().APIToken != token {
			t.Fatal("configured API token was not preserved")
		}
	}
	for _, token := range []string{"short", strings.Repeat("a", 513), strings.Repeat("a", 32) + "\n", strings.Repeat("a", 32) + " "} {
		func() {
			t.Setenv("MCSTATUS_API_TOKEN", token)
			defer func() {
				if recover() == nil {
					t.Error("invalid API token did not stop startup")
				}
			}()
			Load()
		}()
	}
}
