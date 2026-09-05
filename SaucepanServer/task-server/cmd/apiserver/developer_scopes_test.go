package main

import "testing"

func TestValidateAPIKeyScopes(t *testing.T) {
	if err := validateAPIKeyScopes([]string{devScopeTasksRead, devScopeTasksWrite, devScopeStatusRead}); err != nil {
		t.Fatalf("valid scopes rejected: %v", err)
	}
	if err := validateAPIKeyScopes([]string{"admin:*"}); err == nil {
		t.Fatal("unknown scope accepted")
	}
}
