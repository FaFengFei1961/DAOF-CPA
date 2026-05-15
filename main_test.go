package main

import "testing"

func TestGetCORSOrigins_EnvOverride(t *testing.T) {
	t.Setenv("DAOF_CORS_ALLOWED_ORIGINS", "https://admin.example.com")
	if got := getCORSOrigins(); got != "https://admin.example.com" {
		t.Fatalf("origins=%q want env override", got)
	}

	t.Setenv("DAOF_CORS_ALLOWED_ORIGINS", "")
	wantDefault := "http://localhost:3000, http://127.0.0.1:3000"
	if got := getCORSOrigins(); got != wantDefault {
		t.Fatalf("default origins=%q want %q", got, wantDefault)
	}
}
