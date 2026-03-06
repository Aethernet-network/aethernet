package platform

import (
	"strings"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	km := NewKeyManager()
	key := km.GenerateKey("Test App", "test@example.com", TierFree)
	if key == nil {
		t.Fatal("expected non-nil key")
	}
	if !strings.HasPrefix(key.Key, "aet_") {
		t.Errorf("key prefix: got %q, want aet_...", key.Key[:min(8, len(key.Key))])
	}
	if len(key.Key) != 68 { // "aet_" (4) + hex(32 bytes) (64)
		t.Errorf("key length = %d, want 68", len(key.Key))
	}
	if !key.Active {
		t.Error("new key should be active")
	}
	if key.Name != "Test App" {
		t.Errorf("name = %q, want %q", key.Name, "Test App")
	}
	if key.Tier != TierFree {
		t.Errorf("tier = %q, want %q", key.Tier, TierFree)
	}
	if key.OwnerEmail != "test@example.com" {
		t.Errorf("email = %q, want test@example.com", key.OwnerEmail)
	}
	if key.CreatedAt == 0 {
		t.Error("CreatedAt should be non-zero")
	}
}

func TestValidate(t *testing.T) {
	km := NewKeyManager()
	key := km.GenerateKey("App", "user@example.com", TierDeveloper)

	got, ok := km.Validate(key.Key)
	if !ok {
		t.Fatal("Validate returned false for valid key")
	}
	if got.RequestCount != 1 {
		t.Errorf("RequestCount after 1st validate = %d, want 1", got.RequestCount)
	}

	km.Validate(key.Key) //nolint:errcheck
	km.Validate(key.Key) //nolint:errcheck

	got2, ok2 := km.GetKey(key.Key)
	if !ok2 {
		t.Fatal("GetKey returned false for known key")
	}
	if got2.RequestCount != 3 {
		t.Errorf("RequestCount after 3 validates = %d, want 3", got2.RequestCount)
	}
}

func TestValidate_Invalid(t *testing.T) {
	km := NewKeyManager()
	_, ok := km.Validate("aet_doesnotexist")
	if ok {
		t.Error("Validate returned true for unknown key")
	}
}

func TestRevoke(t *testing.T) {
	km := NewKeyManager()
	key := km.GenerateKey("App", "user@example.com", TierEnterprise)

	if !km.Revoke(key.Key) {
		t.Fatal("Revoke returned false for valid key")
	}
	_, ok := km.Validate(key.Key)
	if ok {
		t.Error("Validate returned true for revoked key, want false")
	}
	// Revoking unknown key returns false.
	if km.Revoke("aet_unknown") {
		t.Error("Revoke returned true for unknown key")
	}
}

func TestRateLimit(t *testing.T) {
	cases := []struct {
		tier  Tier
		limit int
	}{
		{TierFree, 100},
		{TierDeveloper, 1000},
		{TierEnterprise, 10000},
		{"unknown", 100},
	}
	for _, c := range cases {
		if got := RateLimit(c.tier); got != c.limit {
			t.Errorf("RateLimit(%q) = %d, want %d", c.tier, got, c.limit)
		}
	}
}

func TestStats(t *testing.T) {
	km := NewKeyManager()
	km.GenerateKey("App1", "a@example.com", TierFree)
	k2 := km.GenerateKey("App2", "b@example.com", TierDeveloper)
	km.Revoke(k2.Key)

	stats := km.Stats()
	if stats["total_keys"] != 2 {
		t.Errorf("total_keys = %v, want 2", stats["total_keys"])
	}
	if stats["active_keys"] != 1 {
		t.Errorf("active_keys = %v, want 1", stats["active_keys"])
	}
	if stats["total_requests"] != uint64(0) {
		t.Errorf("total_requests = %v, want 0", stats["total_requests"])
	}
}

