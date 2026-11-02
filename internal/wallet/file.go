package wallet

import (
	"fmt"
	"os"
)

// Unlock decrypts the keystore file at path.
func Unlock(path, passphrase string) (*Key, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wallet: reading keystore: %w", err)
	}
	return Decrypt(blob, passphrase)
}

// Create writes a fresh key to path; refuses to overwrite.
func Create(path, passphrase string) (*Key, error) {
	k, err := Generate()
	if err != nil {
		return nil, err
	}
	return k, k.save(path, passphrase)
}

// Import writes an existing private key (hex) to path; refuses to overwrite.
func Import(path, privHex, passphrase string) (*Key, error) {
	k, err := FromHex(privHex)
	if err != nil {
		return nil, err
	}
	return k, k.save(path, passphrase)
}

func (k *Key) save(path, passphrase string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("wallet: %s already exists; refusing to overwrite", path)
	}
	blob, err := k.Encrypt(passphrase)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return fmt.Errorf("wallet: writing keystore: %w", err)
	}
	return nil
}
