package auth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/auth"
)

// Cross-language test vectors. These are the ground truth for both Go and
// Python SDK canonicalization. If you change a vector here, update
// sdk/python/tests/test_canonical_crosslang.py to match.

func TestCanonicalizeJSON_CrossLanguageVectors(t *testing.T) {
	vectors := []struct {
		name       string
		input      string
		wantBytes  string
		wantSHA256 string
	}{
		{
			name:       "simple_key_sorting",
			input:      `{"b":2,"a":1}`,
			wantBytes:  `{"a":1,"b":2}`,
			wantSHA256: "43258cff783fe7036d8a43033f830adfc60ec037382473548ac742b888292777",
		},
		{
			name:       "nested_recursive_sorting",
			input:      `{"outer":{"z":1,"a":2},"inner":[3,1,2]}`,
			wantBytes:  `{"inner":[3,1,2],"outer":{"a":2,"z":1}}`,
			wantSHA256: "ab9eb50b92d069549a6e6b1e93380f070804e6619e0a5ccc62cd8717dfef71ad",
		},
		{
			name:       "integer_zero",
			input:      `{"val":0}`,
			wantBytes:  `{"val":0}`,
			wantSHA256: "3d327872b987fdbbdece95d0d7bec019bb18cf392356e7e155a3606a1415070a",
		},
		{
			name:       "integer_negative",
			input:      `{"val":-1}`,
			wantBytes:  `{"val":-1}`,
			wantSHA256: "bd78e10a2d0a9da9baed04b8cc9779b555f2d40c42115ca3d4e0bd1cad70a373",
		},
		{
			name:       "integer_large",
			input:      `{"val":1000000000000}`,
			wantBytes:  `{"val":1000000000000}`,
			wantSHA256: "e4064fa9f5d6cc73cdf030ef71ab5ca9ac578375ddbec4f62b71e0f18ef7190b",
		},
		{
			name:       "empty_object",
			input:      `{}`,
			wantBytes:  `{}`,
			wantSHA256: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
		},
		{
			name:       "empty_string_value",
			input:      `{"a":""}`,
			wantBytes:  `{"a":""}`,
			wantSHA256: "258555fe010df3da34b3920945d0fbc59cebbcff1878bfc2e9206f0f495d81b9",
		},
		{
			name:       "empty_array_value",
			input:      `{"a":[]}`,
			wantBytes:  `{"a":[]}`,
			wantSHA256: "50e8660084976a10f0b3b9b3a6352d5881cbd219b5587a26224971a60ff2cc55",
		},
		{
			name:       "unicode_trademark",
			input:      `{"name":"AetherNet™"}`,
			wantBytes:  `{"name":"AetherNet™"}`,
			wantSHA256: "55c82259eef59b017eb24c59977a1cb0c587643a417b3a834dbea6e3758bb90d",
		},
		{
			name:       "boolean_and_null",
			input:      `{"flag":true,"nothing":null}`,
			wantBytes:  `{"flag":true,"nothing":null}`,
			wantSHA256: "aecd989457f6d1603fe9edf7a9908fc933c9948ecd649e903e5091a13066eab3",
		},
		{
			name:       "tx_v1_payload",
			input:      `{"version":"AETHERNET-TX-V1","chain_id":"aethernet-testnet-1","actor":"abc123def456","method":"POST","path":"/v1/agents/register","body_sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","created_at":1700000000,"expires_at":1700000120,"nonce":"deadbeef01234567"}`,
			wantBytes:  `{"actor":"abc123def456","body_sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","chain_id":"aethernet-testnet-1","created_at":1700000000,"expires_at":1700000120,"method":"POST","nonce":"deadbeef01234567","path":"/v1/agents/register","version":"AETHERNET-TX-V1"}`,
			wantSHA256: "25c54f03e0a7e80a53bf0fd291b4d3e720e6d9f7049091992638ebffbe9100cf",
		},
	}

	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			got, err := auth.CanonicalizeJSON([]byte(v.input))
			if err != nil {
				t.Fatalf("CanonicalizeJSON: %v", err)
			}
			if string(got) != v.wantBytes {
				t.Errorf("bytes:\n  got:  %s\n  want: %s", got, v.wantBytes)
			}
			h := sha256.Sum256(got)
			gotHash := hex.EncodeToString(h[:])
			if gotHash != v.wantSHA256 {
				t.Errorf("SHA-256:\n  got:  %s\n  want: %s", gotHash, v.wantSHA256)
			}
		})
	}
}
