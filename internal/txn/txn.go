// Package txn is the prepare → (caller confirms) → sign → send flow shared by
// deploy and mint. Prepare signs nothing, so callers can show cost first.
package txn

import (
	"context"
	"math/big"
	"time"

	"github.com/smhaaker/ac-evm/internal/chain"
	"github.com/smhaaker/ac-evm/internal/eth"
	"github.com/smhaaker/ac-evm/internal/wallet"
)

// 20% headroom on the gas estimate.
const (
	gasHeadroomNum = 12
	gasHeadroomDen = 10
)

// Prepared is a transaction costed and ready to sign.
type Prepared struct {
	ChainID  uint64
	Nonce    uint64
	Gas      uint64
	GasPrice *big.Int
	To       string // empty for contract creation
	Data     []byte
}

// MaxCost is the worst-case fee in wei.
func (p *Prepared) MaxCost() *big.Int {
	return new(big.Int).Mul(new(big.Int).SetUint64(p.Gas), p.GasPrice)
}

// Prepare reads chain id, nonce, gas price and a padded gas estimate.
func Prepare(ctx context.Context, c *chain.Client, from, to string, data []byte) (*Prepared, error) {
	chainID, err := c.ChainID(ctx)
	if err != nil {
		return nil, err
	}
	nonce, err := c.Nonce(ctx, from)
	if err != nil {
		return nil, err
	}
	gasPrice, err := c.GasPrice(ctx)
	if err != nil {
		return nil, err
	}
	gas, err := c.EstimateGas(ctx, from, to, data, nil)
	if err != nil {
		return nil, err
	}
	return &Prepared{
		ChainID:  chainID,
		Nonce:    nonce,
		Gas:      gas * gasHeadroomNum / gasHeadroomDen,
		GasPrice: gasPrice,
		To:       to,
		Data:     data,
	}, nil
}

// Send signs, broadcasts, and waits for the receipt.
func Send(ctx context.Context, c *chain.Client, k *wallet.Key, p *Prepared) (*chain.Receipt, string, error) {
	tx := eth.Tx{
		ChainID: p.ChainID,
		Nonce:   p.Nonce,
		Tip:     p.GasPrice, // tip == cap: priced like a legacy tx
		FeeCap:  p.GasPrice,
		Gas:     p.Gas,
		Value:   new(big.Int),
		Data:    p.Data,
	}
	if p.To != "" {
		to, err := eth.ParseAddress(p.To)
		if err != nil {
			return nil, "", err
		}
		tx.To = &to
	}
	raw, err := k.SignTx(tx)
	if err != nil {
		return nil, "", err
	}
	hash, err := c.SendRawTransaction(ctx, raw)
	if err != nil {
		return nil, hash, err
	}
	rcpt, err := c.WaitReceipt(ctx, hash, 3*time.Second)
	if err != nil {
		return nil, hash, err
	}
	return rcpt, hash, nil
}
