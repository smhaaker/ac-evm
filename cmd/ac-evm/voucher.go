package main

// Sign-later: sign a mint voucher (no gas), or submit someone's voucher (you pay gas).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/smhaaker/ac-evm/internal/ac"
	"github.com/smhaaker/ac-evm/internal/chain"
	"github.com/smhaaker/ac-evm/internal/eth"
)

const voucherTTL = 30 * 24 * time.Hour

type signReq struct {
	text                     string
	blob                     []byte
	chainID, nonce, deadline uint64
}

type signPrepMsg struct {
	req *signReq
	err error
}

func (m model) startSign(text string) (tea.Model, tea.Cmd) {
	req := &signReq{text: text, blob: ac.PackBest([]byte(text)), deadline: uint64(time.Now().Add(voucherTTL).Unix())}
	rpc, nft, me := m.cfg.RPC, m.cfg.NFT, m.wallet.Address().Hex()
	m.sign, m.screen, m.busy, m.status, m.err = nil, screenSign, true, "reading nonce…", nil
	return m, func() tea.Msg {
		c, err := chain.New(rpc, nft)
		if err != nil {
			return signPrepMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if req.chainID, err = c.ChainID(ctx); err != nil {
			return signPrepMsg{err: err}
		}
		if req.nonce, err = c.Nonces(ctx, me); err != nil {
			return signPrepMsg{err: err}
		}
		return signPrepMsg{req: req}
	}
}

func (m model) updateSign(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "n", "N", "esc", "q":
		m.sign, m.busy = nil, false
		m.screen = screenCompose
		return m, nil
	case "y", "Y":
		if m.busy || m.sign == nil {
			return m, nil
		}
		r := m.sign
		tok, err := eth.ParseAddress(m.cfg.NFT)
		if err != nil {
			m.err = err
			return m, nil
		}
		sig, err := m.wallet.Sign(eth.MintDigest(r.chainID, tok, m.wallet.Address(), r.blob, r.nonce, r.deadline))
		if err != nil {
			m.err = err
			return m, nil
		}
		m.voucher = eth.Voucher{Author: m.wallet.Address(), Deadline: r.deadline, Sig: sig, Blob: r.blob}.String()
		m.voucherPath = filepath.Join(m.dir, "vouchers", fmt.Sprintf("nonce-%d.txt", r.nonce))
		if err := os.MkdirAll(filepath.Dir(m.voucherPath), 0o700); err == nil {
			err = os.WriteFile(m.voucherPath, []byte(m.voucher+"\n"), 0o600)
		}
		if err != nil {
			m.err, m.voucherPath = err, ""
		}
		m.sign = nil
		m.screen = screenVoucher
	}
	return m, nil
}

func (m model) viewSign() string {
	b := titleStyle.Render("sign a mint voucher") + "\n\n"
	r := m.sign
	if r == nil {
		if m.err != nil {
			return b + errStyle.Render(m.err.Error()) + "\n\n" + hintStyle.Render("esc: back")
		}
		return b + hintStyle.Render("reading nonce…") + "\n\n" + hintStyle.Render("esc: cancel")
	}
	b += hintStyle.Render("you are signing:") + "\n" + r.text + "\n\n"
	b += fmt.Sprintf("  stored:    %d bytes (%s prior)\n", len(r.blob), ac.PriorName(r.blob[0]))
	b += fmt.Sprintf("  chain:     %d%s\n", r.chainID, fujiNote(r.chainID))
	b += fmt.Sprintf("  contract:  %s\n", m.cfg.NFT)
	b += fmt.Sprintf("  nonce:     %d\n", r.nonce)
	b += fmt.Sprintf("  expires:   %s\n", time.Unix(int64(r.deadline), 0).Format("2006-01-02 15:04"))
	b += "\n" + okStyle.Render("no gas — anyone can submit this, and the token always goes to you") + "\n" +
		hintStyle.Render("vouchers are single-use per nonce: get this one submitted before signing another") + "\n"
	if m.err != nil {
		b += "\n" + errStyle.Render(m.err.Error()) + "\n"
	}
	return b + "\n" + hintStyle.Render("y: sign   n/esc: cancel")
}

func (m model) viewVoucher() string {
	b := titleStyle.Render("voucher signed") + "\n\n" +
		hintStyle.Render("give this to whoever will submit it (gallery → v):") + "\n" +
		wrapText(m.voucher, m.graphWidth()) + "\n"
	if m.voucherPath != "" {
		b += "\n" + okStyle.Render("saved to "+m.voucherPath) + "\n"
	}
	if m.err != nil {
		b += "\n" + errStyle.Render(m.err.Error()) + "\n"
	}
	return b + "\n" + hintStyle.Render("esc: back")
}

func (m model) openSubmit() (tea.Model, tea.Cmd) {
	if m.cfg.NFT == "" {
		m.err = errors.New("no NFT deployed — press d to deploy first")
		return m, nil
	}
	m.voucherIn.Reset()
	m.voucherIn.Focus()
	m.submitV, m.submitText, m.err = nil, "", nil
	m.screen = screenSubmit
	return m, textinput.Blink
}

func (m model) updateSubmit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.screen = screenGallery
		return m, nil
	case tea.KeyEnter:
		if m.submitV == nil {
			return m.previewVoucher()
		}
		v := m.submitV
		m.pending = &pendingTx{
			kind:    txMintSig,
			to:      m.cfg.NFT,
			data:    chain.MintWithSigCalldata(v.Author.Hex(), v.Blob, v.Deadline, v.Sig[:]),
			summary: fmt.Sprintf("submit voucher: mint %d chars for %s (you pay gas)", len(m.submitText), v.Author.Hex()),
		}
		return m.toConfirm()
	}
	m.submitV = nil // any edit invalidates the preview
	var cmd tea.Cmd
	m.voucherIn, cmd = m.voucherIn.Update(msg)
	return m, cmd
}

func (m model) previewVoucher() (tea.Model, tea.Cmd) {
	v, err := eth.ParseVoucher(m.voucherIn.Value())
	if err != nil {
		m.err = err
		return m, nil
	}
	_, out, err := ac.Unpack(v.Blob, maxText)
	if err != nil {
		m.err = fmt.Errorf("voucher text does not decode: %w", err)
		return m, nil
	}
	if time.Now().Unix() > int64(v.Deadline) {
		m.err = errors.New("voucher has expired")
		return m, nil
	}
	m.submitV, m.submitText, m.err = &v, string(out), nil
	return m, nil
}

func (m model) viewSubmit() string {
	b := titleStyle.Render("submit a voucher") + "\n\n" +
		hintStyle.Render("paste an acevm: voucher; the token is minted to its author, you pay the gas") + "\n\n" +
		m.voucherIn.View() + "\n"
	if m.err != nil {
		b += "\n" + errStyle.Render(m.err.Error()) + "\n"
	}
	if v := m.submitV; v != nil {
		b += "\n" + fmt.Sprintf("  author:   %s\n", v.Author.Hex()) +
			fmt.Sprintf("  stored:   %d bytes (%s prior)\n", len(v.Blob), ac.PriorName(v.Blob[0])) +
			fmt.Sprintf("  expires:  %s\n", time.Unix(int64(v.Deadline), 0).Format("2006-01-02 15:04")) +
			"\n" + hintStyle.Render("decoded:") + "\n" + m.submitText + "\n" +
			"\n" + hintStyle.Render("enter: review gas & submit   esc: cancel")
		return b
	}
	return b + "\n" + hintStyle.Render("enter: check voucher   esc: cancel")
}

func wrapText(s string, width int) string {
	var b strings.Builder
	for len(s) > width {
		b.WriteString(s[:width] + "\n")
		s = s[width:]
	}
	return b.String() + s
}
