package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"
)

// WalletFile is the on-disk encrypted key format.
type WalletFile struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	AgentID   string `json:"agent_id"`
	PublicKey string `json:"public_key_hex"`
	Salt      string `json:"salt"`
	Nonce     string `json:"nonce"`
	Encrypted string `json:"encrypted"`
	CreatedAt string `json:"created_at"`
}

// configFile stores the active wallet selection.
type configFile struct {
	ActiveWallet string `json:"active_wallet"`
}

func keysDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aethernet", "keys")
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aethernet", "config.json")
}

func walletPath(name string) string {
	return filepath.Join(keysDir(), name+".json")
}

func ensureKeysDir() {
	_ = os.MkdirAll(keysDir(), 0700)
}

func promptPassword(prompt string) (string, error) {
	// Check environment variable first (CI/scripts).
	if pw := os.Getenv("AETHERNET_KEY_PASSWORD"); pw != "" {
		return pw, nil
	}
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}

func encryptSeed(seed []byte, password string) (salt, nonce, encrypted string, err error) {
	saltBytes := make([]byte, 32)
	if _, err = rand.Read(saltBytes); err != nil {
		return
	}
	key, err := scrypt.Key([]byte(password), saltBytes, 1<<15, 8, 1, 32)
	if err != nil {
		return
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return
	}
	nonceBytes := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonceBytes); err != nil {
		return
	}
	ct := gcm.Seal(nil, nonceBytes, seed, nil)
	return hex.EncodeToString(saltBytes), hex.EncodeToString(nonceBytes), hex.EncodeToString(ct), nil
}

func decryptSeed(saltHex, nonceHex, encryptedHex, password string) ([]byte, error) {
	saltBytes, err := hex.DecodeString(saltHex)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	ct, err := hex.DecodeString(encryptedHex)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}

	key, err := scrypt.Key([]byte(password), saltBytes, 1<<15, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	seed, err := gcm.Open(nil, nonceBytes, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt failed (wrong password?)")
	}
	return seed, nil
}

func createWallet(name string) (*WalletFile, ed25519.PrivateKey, error) {
	ensureKeysDir()

	if name == "" {
		name = "default"
	}
	if _, err := os.Stat(walletPath(name)); err == nil {
		return nil, nil, fmt.Errorf("wallet %q already exists", name)
	}

	pw, err := promptPassword("Set password: ")
	if err != nil {
		return nil, nil, err
	}
	if pw == "" {
		return nil, nil, fmt.Errorf("password cannot be empty")
	}
	pw2, err := promptPassword("Confirm password: ")
	if err != nil {
		return nil, nil, err
	}
	if pw != pw2 {
		return nil, nil, fmt.Errorf("passwords do not match")
	}

	// Generate Ed25519 keypair.
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, nil, err
	}
	pk := ed25519.NewKeyFromSeed(seed)
	pub := pk.Public().(ed25519.PublicKey)
	agentID := hex.EncodeToString(pub)

	salt, nonce, encrypted, err := encryptSeed(seed, pw)
	if err != nil {
		return nil, nil, err
	}

	wf := &WalletFile{
		Version:   1,
		Name:      name,
		AgentID:   agentID,
		PublicKey: hex.EncodeToString(pub),
		Salt:      salt,
		Nonce:     nonce,
		Encrypted: encrypted,
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	data, _ := json.MarshalIndent(wf, "", "  ")
	if err := os.WriteFile(walletPath(name), data, 0600); err != nil {
		return nil, nil, err
	}

	// Set as active wallet.
	setActiveWallet(name)

	return wf, pk, nil
}

func loadWallet(name string) (*WalletFile, error) {
	data, err := os.ReadFile(walletPath(name))
	if err != nil {
		return nil, fmt.Errorf("wallet %q not found", name)
	}
	var wf WalletFile
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	return &wf, nil
}

func unlockWallet(wf *WalletFile) (ed25519.PrivateKey, error) {
	pw, err := promptPassword(fmt.Sprintf("Password for %s: ", wf.Name))
	if err != nil {
		return nil, err
	}
	seed, err := decryptSeed(wf.Salt, wf.Nonce, wf.Encrypted, pw)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func listWallets() ([]WalletFile, error) {
	entries, err := os.ReadDir(keysDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var wallets []WalletFile
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(keysDir(), e.Name()))
		if err != nil {
			continue
		}
		var wf WalletFile
		if json.Unmarshal(data, &wf) == nil && wf.Version > 0 {
			wallets = append(wallets, wf)
		}
	}
	return wallets, nil
}

func activeWalletName() string {
	data, err := os.ReadFile(configPath())
	if err != nil {
		return "default"
	}
	var cfg configFile
	if json.Unmarshal(data, &cfg) == nil && cfg.ActiveWallet != "" {
		return cfg.ActiveWallet
	}
	return "default"
}

func setActiveWallet(name string) {
	home, _ := os.UserHomeDir()
	_ = os.MkdirAll(filepath.Join(home, ".aethernet"), 0700)
	cfg := configFile{ActiveWallet: name}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(configPath(), data, 0600)
}

func getActiveWallet() (*WalletFile, error) {
	name := activeWalletName()
	return loadWallet(name)
}

// registerAgentOnTestnet calls POST /v1/agents with the wallet's public key.
func registerAgentOnTestnet(nodeURL string, wf *WalletFile) (map[string]any, error) {
	pubBytes, _ := hex.DecodeString(wf.PublicKey)
	pubB64 := base64.StdEncoding.EncodeToString(pubBytes)

	var result map[string]any
	err := apiPost(nodeURL, "/v1/agents", map[string]any{
		"agent_id":       wf.AgentID,
		"public_key_b64": pubB64,
	}, &result)
	return result, err
}

func exportWallet(name, outPath string) error {
	data, err := os.ReadFile(walletPath(name))
	if err != nil {
		return fmt.Errorf("wallet %q not found", name)
	}
	return os.WriteFile(outPath, data, 0600)
}

func importWallet(srcPath string) error {
	ensureKeysDir()
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	var wf WalletFile
	if err := json.Unmarshal(data, &wf); err != nil {
		return fmt.Errorf("invalid wallet file: %w", err)
	}
	if wf.Name == "" {
		wf.Name = "imported"
	}

	// Verify decryption works.
	pw, err := promptPassword("Password to decrypt: ")
	if err != nil {
		return err
	}
	if _, err := decryptSeed(wf.Salt, wf.Nonce, wf.Encrypted, pw); err != nil {
		return err
	}

	dest := walletPath(wf.Name)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("wallet %q already exists", wf.Name)
	}
	return os.WriteFile(dest, data, 0600)
}
