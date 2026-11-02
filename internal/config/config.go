// Package config is config.json in the state dir: RPC endpoint and contracts.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/smhaaker/ac-evm/internal/chain"
)

// Config is persisted as config.json in the state directory.
type Config struct {
	RPC     string `json:"rpc"`
	Decoder string `json:"decoder"` // ArithmeticDecoder, empty until deployed
	NFT     string `json:"nft"`     // ACToken, empty until deployed
}

func Path(dir string) string { return filepath.Join(dir, "config.json") }

// Load returns defaults (Fuji, no contracts) when config.json is missing.
func Load(dir string) (*Config, error) {
	c := &Config{RPC: chain.FujiRPC}
	b, err := os.ReadFile(Path(dir))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: reading: %w", err)
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("config: %s is corrupt: %w", Path(dir), err)
	}
	if c.RPC == "" {
		c.RPC = chain.FujiRPC
	}
	return c, nil
}

func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(Path(dir), b, 0o600); err != nil {
		return fmt.Errorf("config: writing: %w", err)
	}
	return nil
}
