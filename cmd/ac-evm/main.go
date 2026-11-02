// Command ac-evm is the TUI: deploy, mint (compressed here, decoded on-chain),
// list and read your tokens, and visualize the coder. Every signed transaction
// is costed and held behind a y/N confirm.
//
//	ac-evm                 unlock ~/.ac-evm/keystore.json (create if absent)
//	ac-evm -dir path       use a different state directory
//	ac-evm -rpc url        override the JSON-RPC endpoint
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/smhaaker/ac-evm/internal/ac"
	"github.com/smhaaker/ac-evm/internal/chain"
	"github.com/smhaaker/ac-evm/internal/config"
	"github.com/smhaaker/ac-evm/internal/eth"
	"github.com/smhaaker/ac-evm/internal/txn"
	"github.com/smhaaker/ac-evm/internal/wallet"
)

func main() {
	dir := flag.String("dir", defaultDir(), "state directory holding keystore.json and config.json")
	artifact := flag.String("out", "out", "directory of compiled .bin files (make compile)")
	rpc := flag.String("rpc", "", "JSON-RPC endpoint (overrides config.json)")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "ac-evm:", err)
		os.Exit(1)
	}
	m, err := initialModel(*dir, *artifact, *rpc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ac-evm:", err)
		os.Exit(1)
	}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ac-evm:", err)
		os.Exit(1)
	}
}

// defaultDir is $ACEVM_DIR, else ~/.ac-evm.
func defaultDir() string {
	if d := os.Getenv("ACEVM_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ac-evm"
	}
	return filepath.Join(home, ".ac-evm")
}

type screen int

const (
	screenUnlock screen = iota
	screenImport        // paste an existing private key
	screenGallery
	screenCompose // mint
	screenReader  // view a decoded message
	screenDecode  // paste a coded hex message and decode it locally
	screenSteps   // step-by-step interval subdivision (canonical AC view)
	screenConfirm
	screenSign    // review + sign a mint voucher
	screenVoucher // show the signed voucher
	screenSubmit  // paste someone's voucher
)

type txKind int

const (
	txDeployDecoder txKind = iota
	txDeployToken
	txMint
	txMintSig
)

// maxText mirrors MAX_OUT in ACToken.sol.
const maxText = 1000

type token struct {
	ID    uint64
	First string
	Badge string // prior + stored size, e.g. "eng 22B"
}

type pendingTx struct {
	kind    txKind
	to      string // "" for deploy
	data    []byte
	summary string
	prep    *txn.Prepared
}

type model struct {
	screen       screen
	dir          string
	artifact     string
	keystorePath string
	exists       bool
	cfg          *config.Config
	pass         textinput.Model
	wallet       *wallet.Key

	tokens    []token
	cursor    int
	reader    string // decoded message on the reader screen
	readerEnc []byte // the stored compressed bytes for that token

	compose     textinput.Model
	importKey   textinput.Model
	importFocus int // 0 = key, 1 = passphrase

	decodeIn  textinput.Model // coded hex to decode locally
	decodeRaw []byte          // parsed coded bytes
	decodeOut string          // decoded plaintext

	stepsText  string          // message whose interval steps we're showing
	stepsCosts []ac.SymbolCost // cached per-symbol analysis (correct prior)
	stepsPrior string          // prior label for the header
	stepsPos   int             // current step (the cursor the animation drives)
	stepsPlay  bool            // auto-advance on ticks
	stepsBack  screen          // where esc returns to

	sign        *signReq
	voucher     string
	voucherPath string
	voucherIn   textinput.Model
	submitV     *eth.Voucher
	submitText  string

	pending *pendingTx
	busy    bool
	status  string
	err     error
	spinner spinner.Model
	width   int
	height  int
}

func initialModel(dir, artifact, rpc string) (model, error) {
	cfg, err := config.Load(dir)
	if err != nil {
		return model{}, err
	}
	if rpc != "" {
		cfg.RPC = rpc
	}
	keystorePath := filepath.Join(dir, "keystore.json")
	_, statErr := os.Stat(keystorePath)

	pass := textinput.New()
	pass.Placeholder = "passphrase"
	pass.EchoMode = textinput.EchoPassword
	pass.EchoCharacter = '•'
	pass.Focus()
	pass.CharLimit = 256

	compose := textinput.New()
	compose.Placeholder = "your message"
	compose.CharLimit = maxText
	compose.Prompt = "msg: "

	importKey := textinput.New()
	importKey.Placeholder = "private key hex (0x…)"
	importKey.EchoMode = textinput.EchoPassword
	importKey.EchoCharacter = '•'
	importKey.CharLimit = 66
	importKey.Prompt = "key:  "

	decodeIn := textinput.New()
	decodeIn.Placeholder = "coded message hex (0x…)"
	decodeIn.CharLimit = 65536
	decodeIn.Prompt = "hex: "

	voucherIn := textinput.New()
	voucherIn.Placeholder = "acevm:…"
	voucherIn.CharLimit = 8192
	voucherIn.Prompt = "voucher: "

	sp := spinner.New()
	sp.Spinner = spinner.Points
	sp.Style = lipgloss.NewStyle().Foreground(cCyan)

	return model{
		spinner:      sp,
		screen:       screenUnlock,
		dir:          dir,
		artifact:     artifact,
		keystorePath: keystorePath,
		exists:       statErr == nil,
		cfg:          cfg,
		pass:         pass,
		compose:      compose,
		importKey:    importKey,
		decodeIn:     decodeIn,
		voucherIn:    voucherIn,
	}, nil
}

func (m model) Init() tea.Cmd { return tea.Batch(textinput.Blink, m.spinner.Tick) }

type galleryMsg struct {
	tokens []token
	err    error
}
type readMsg struct {
	text    string
	encoded []byte
	err     error
}
type preparedMsg struct {
	prep *txn.Prepared
	err  error
}
type txDoneMsg struct {
	kind txKind
	addr string // deployed contract address, for deploy
	err  error
}

// loadGalleryCmd lists the newest tokens owned by this wallet.
func (m model) loadGalleryCmd() tea.Cmd {
	rpc, nft, me := m.cfg.RPC, m.cfg.NFT, strings.ToLower(m.wallet.Address().Hex())
	return func() tea.Msg {
		if nft == "" {
			return galleryMsg{}
		}
		c, err := chain.New(rpc, nft)
		if err != nil {
			return galleryMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		total, err := c.TotalSupply(ctx)
		if err != nil {
			return galleryMsg{err: err}
		}
		var toks []token
		// newest first, cap the scan
		const cap = 200
		for id := total; id >= 1 && len(toks) < cap; id-- {
			owner, err := c.OwnerOf(ctx, id)
			if err != nil {
				continue
			}
			if strings.ToLower(owner) != me {
				continue
			}
			first, badge := "", ""
			if msg, err := c.ReadMessage(ctx, id); err == nil {
				first = firstLine(msg)
				if enc, err := c.CompressedOf(ctx, id); err == nil {
					badge = priorBadge(msg, enc)
				}
			}
			toks = append(toks, token{ID: id, First: first, Badge: badge})
		}
		return galleryMsg{tokens: toks}
	}
}

func (m model) readCmd(id uint64) tea.Cmd {
	rpc, nft := m.cfg.RPC, m.cfg.NFT
	return func() tea.Msg {
		c, err := chain.New(rpc, nft)
		if err != nil {
			return readMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text, err := c.ReadMessage(ctx, id)
		if err != nil {
			return readMsg{err: err}
		}
		enc, _ := c.CompressedOf(ctx, id) // the stored bytes, for the proof view
		return readMsg{text: text, encoded: enc}
	}
}

func (m model) prepareCmd(p *pendingTx) tea.Cmd {
	rpc, from := m.cfg.RPC, m.wallet.Address().Hex()
	to, data := p.to, p.data
	return func() tea.Msg {
		c := chain.Dial(rpc) // deploy has no contract; Dial works for all prepares
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		prep, err := txn.Prepare(ctx, c, from, to, data)
		return preparedMsg{prep: prep, err: err}
	}
}

func (m model) sendCmd(p *pendingTx) tea.Cmd {
	rpc, w := m.cfg.RPC, m.wallet
	prep, kind := p.prep, p.kind
	return func() tea.Msg {
		c := chain.Dial(rpc)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		rcpt, _, err := txn.Send(ctx, c, w, prep)
		if err != nil {
			return txDoneMsg{kind: kind, err: err}
		}
		if rcpt.Status != 1 {
			return txDoneMsg{kind: kind, err: errors.New("transaction reverted")}
		}
		return txDoneMsg{kind: kind, addr: rcpt.ContractAddress}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Let the text inputs scroll instead of overflowing the panel.
		iw := m.width - 14
		if iw < 12 {
			iw = 12
		}
		m.compose.Width = iw
		m.decodeIn.Width = iw
		m.voucherIn.Width = iw
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case stepTickMsg:
		if m.screen == screenSteps && m.stepsPlay {
			if m.stepsPos < len(m.stepsCosts)-1 {
				m.stepsPos++
				return m, stepTick()
			}
			m.stepsPlay = false // reached the end
		}
		return m, nil

	case signPrepMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.sign, m.status = msg.req, ""
		return m, nil

	case galleryMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.tokens = msg.tokens
		if m.cursor >= len(m.tokens) {
			m.cursor = 0
		}
		m.status = fmt.Sprintf("%d token(s) you own", len(m.tokens))
		return m, nil

	case readMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			m.screen = screenGallery
			return m, nil
		}
		m.reader = msg.text
		m.readerEnc = msg.encoded
		m.screen = screenReader
		return m, nil

	case preparedMsg:
		m.busy = false
		if msg.err != nil {
			m.pending = nil
			m.screen = screenGallery
			m.err, m.status = chain.ExplainRevert(msg.err), ""
			return m, nil
		}
		if m.pending != nil {
			m.pending.prep = msg.prep
		}
		return m, nil

	case txDoneMsg:
		m.busy = false
		p := m.pending
		m.pending = nil
		m.screen = screenGallery
		if msg.err != nil {
			m.err, m.status = chain.ExplainRevert(msg.err), ""
			return m, nil
		}
		m.err = nil
		switch {
		case p != nil && p.kind == txDeployDecoder:
			m.cfg.Decoder = msg.addr
			if err := m.cfg.Save(m.dir); err != nil {
				m.err = err
				return m, nil
			}
			m.status = "decoder deployed " + msg.addr
			return m.startDeploy() // step 2: the token, behind its own confirm
		case p != nil && p.kind == txDeployToken:
			m.cfg.NFT = msg.addr
			if err := m.cfg.Save(m.dir); err != nil {
				m.err = err
			}
			m.status = "deployed " + msg.addr
		case p != nil && p.kind == txMintSig:
			m.status = "minted for the voucher's author — refreshing"
		default:
			m.status = "minted — refreshing"
		}
		m.busy = true
		return m, m.loadGalleryCmd()

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		switch m.screen {
		case screenUnlock:
			return m.updateUnlock(msg)
		case screenImport:
			return m.updateImport(msg)
		case screenGallery:
			return m.updateGallery(msg)
		case screenCompose:
			return m.updateCompose(msg)
		case screenReader:
			switch msg.String() {
			case "q", "esc":
				m.screen = screenGallery
			case "d":
				// push the shown coded bytes into the decoder, prefilled
				return m.openDecode("0x" + hex.EncodeToString(m.readerEnc))
			case "i":
				return m.openSteps(m.reader, m.readerEnc, screenReader)
			}
			return m, nil
		case screenDecode:
			return m.updateDecode(msg)
		case screenSteps:
			return m.updateSteps(msg)
		case screenConfirm:
			return m.updateConfirm(msg)
		case screenSign:
			return m.updateSign(msg)
		case screenVoucher:
			if msg.String() == "esc" || msg.String() == "q" || msg.Type == tea.KeyEnter {
				m.screen = screenGallery
			}
			return m, nil
		case screenSubmit:
			return m.updateSubmit(msg)
		}
	}
	return m, nil
}

func (m model) updateUnlock(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		return m.attemptUnlock()
	case tea.KeyEsc:
		return m, tea.Quit
	case tea.KeyCtrlO:
		if !m.exists { // import only makes sense when there is no keystore yet
			m.importKey.Reset()
			m.importFocus = 0
			m.importKey.Focus()
			m.pass.Blur()
			m.screen = screenImport
			m.err = nil
			return m, textinput.Blink
		}
	}
	var cmd tea.Cmd
	m.pass, cmd = m.pass.Update(msg)
	return m, cmd
}

func (m model) updateImport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.pass.Focus()
		m.importKey.Blur()
		m.screen = screenUnlock
		return m, nil
	case tea.KeyTab, tea.KeyShiftTab:
		m.importFocus = 1 - m.importFocus
		if m.importFocus == 0 {
			m.importKey.Focus()
			m.pass.Blur()
		} else {
			m.pass.Focus()
			m.importKey.Blur()
		}
		return m, textinput.Blink
	case tea.KeyEnter:
		return m.attemptImport()
	}
	var cmd tea.Cmd
	if m.importFocus == 0 {
		m.importKey, cmd = m.importKey.Update(msg)
	} else {
		m.pass, cmd = m.pass.Update(msg)
	}
	return m, cmd
}

func (m model) attemptImport() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.importKey.Value()) == "" || m.pass.Value() == "" {
		m.err = errors.New("both a private key and a passphrase are required")
		return m, nil
	}
	w, err := wallet.Import(m.keystorePath, m.importKey.Value(), m.pass.Value())
	if err != nil {
		m.err = err
		m.importKey.Reset()
		return m, nil
	}
	m.importKey.Reset() // don't keep the key in the input buffer
	m.wallet = w
	m.exists = true
	m.err = nil
	m.screen = screenGallery
	m.busy = true
	m.status = "imported — loading…"
	return m, m.loadGalleryCmd()
}

func (m model) updateGallery(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.tokens)-1 {
			m.cursor++
		}
	case "r":
		m.busy, m.status, m.err = true, "loading…", nil
		return m, m.loadGalleryCmd()
	case "d":
		if m.cfg.NFT != "" {
			m.err = errors.New("a contract is already configured for this -dir")
			return m, nil
		}
		return m.startDeploy()
	case "m":
		if m.cfg.NFT == "" {
			m.err = errors.New("no NFT deployed — press d to deploy first")
			return m, nil
		}
		m.compose.Reset()
		m.compose.Focus()
		m.screen = screenCompose
		m.err = nil
		return m, textinput.Blink
	case "enter":
		if len(m.tokens) == 0 {
			return m, nil
		}
		m.busy, m.status = true, "decoding on-chain…"
		return m, m.readCmd(m.tokens[m.cursor].ID)
	case "x":
		return m.openDecode("")
	case "v":
		return m.openSubmit()
	}
	return m, nil
}

// openDecode moves to the decode screen, optionally prefilled with coded hex.
func (m model) openDecode(prefill string) (tea.Model, tea.Cmd) {
	m.decodeIn.SetValue(prefill)
	m.decodeIn.CursorEnd()
	m.decodeIn.Focus()
	m.decodeOut, m.decodeRaw, m.err = "", nil, nil
	m.screen = screenDecode
	return m, textinput.Blink
}

func (m model) updateDecode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.screen = screenGallery
		return m, nil
	case tea.KeyEnter:
		return m.attemptDecode()
	case tea.KeyTab:
		if m.decodeOut != "" { // once decoded, tab opens the interval steps
			return m.openSteps(m.decodeOut, m.decodeRaw, screenDecode)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.decodeIn, cmd = m.decodeIn.Update(msg)
	return m, cmd
}

// detectPrior finds a blob's prior by round-tripping it against the decoded text.
func detectPrior(decoded string, encoded []byte) (payload []byte, prior []byte, label string) {
	if id, out, err := ac.Unpack(encoded, 1<<16); err == nil && string(out) == decoded {
		p, _ := ac.PriorByID(id)
		return encoded[1:], p, ac.PriorName(id)
	}
	return encoded, nil, "cold"
}

// priorBadge is the gallery tag: prior + stored size, e.g. "eng 22B".
func priorBadge(decoded string, encoded []byte) string {
	_, _, label := detectPrior(decoded, encoded)
	if label == "english" {
		label = "eng"
	}
	return fmt.Sprintf("%s %dB", label, len(encoded))
}

// openSteps opens the animated interval view, analyzed with the blob's prior.
func (m model) openSteps(text string, encoded []byte, back screen) (tea.Model, tea.Cmd) {
	var prior []byte
	m.stepsPrior = "cold"
	if encoded != nil {
		_, prior, m.stepsPrior = detectPrior(text, encoded)
	}
	m.stepsText = text
	m.stepsCosts = ac.Analyze([]byte(text), prior)
	m.stepsPos = 0
	m.stepsPlay = false
	m.stepsBack = back
	m.screen = screenSteps
	return m, nil
}

// stepTickMsg drives the decode animation.
type stepTickMsg struct{}

func stepTick() tea.Cmd {
	return tea.Tick(130*time.Millisecond, func(time.Time) tea.Msg { return stepTickMsg{} })
}

func (m model) updateSteps(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.stepsCosts)
	switch msg.String() {
	case "q", "esc":
		m.stepsPlay = false
		m.screen = m.stepsBack
	case " ":
		// play/pause; playing again from the end restarts.
		if !m.stepsPlay && m.stepsPos >= n-1 {
			m.stepsPos = 0
		}
		m.stepsPlay = !m.stepsPlay
		if m.stepsPlay {
			return m, stepTick()
		}
	case "r":
		m.stepsPos = 0
		m.stepsPlay = true
		return m, stepTick()
	case "down", "j", "right", "l":
		m.stepsPlay = false
		if m.stepsPos < n-1 {
			m.stepsPos++
		}
	case "up", "k", "left", "h":
		m.stepsPlay = false
		if m.stepsPos > 0 {
			m.stepsPos--
		}
	case "pgdown":
		m.stepsPlay = false
		m.stepsPos += m.stepsPerPage()
		if m.stepsPos > n-1 {
			m.stepsPos = n - 1
		}
	case "pgup":
		m.stepsPlay = false
		m.stepsPos -= m.stepsPerPage()
		if m.stepsPos < 0 {
			m.stepsPos = 0
		}
	}
	return m, nil
}

// attemptDecode decodes pasted hex locally with the same coder as the contract.
func (m model) attemptDecode() (tea.Model, tea.Cmd) {
	s := strings.TrimSpace(m.decodeIn.Value())
	s = strings.TrimPrefix(s, "0x")
	s = strings.ReplaceAll(s, " ", "")
	raw, err := hex.DecodeString(s)
	if err != nil {
		m.err, m.decodeOut = fmt.Errorf("not valid hex: %w", err), ""
		return m, nil
	}
	_, out, err := ac.Unpack(raw, 1<<16)
	if err != nil {
		m.err, m.decodeOut = fmt.Errorf("decode failed: %w", err), ""
		return m, nil
	}
	m.err = nil
	m.decodeRaw = raw
	m.decodeOut = string(out)
	return m, nil
}

func (m model) updateCompose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.screen = screenGallery
		return m, nil
	case tea.KeyEnter, tea.KeyCtrlS:
		text := m.compose.Value()
		if strings.TrimSpace(text) == "" {
			m.err = errors.New("message is empty")
			return m, nil
		}
		if len(text) > maxText {
			m.err = fmt.Errorf("message is %d bytes; the on-chain cap is %d", len(text), maxText)
			return m, nil
		}
		if msg.Type == tea.KeyCtrlS {
			return m.startSign(text)
		}
		compressed := ac.PackBest([]byte(text))
		m.pending = &pendingTx{
			kind: txMint,
			to:   m.cfg.NFT,
			data: chain.MintCalldata(compressed),
			summary: fmt.Sprintf("mint a %d-byte message (%d packed, %s prior)",
				len(text), len(compressed), ac.PriorName(compressed[0])),
		}
		return m.toConfirm()
	}
	var cmd tea.Cmd
	m.compose, cmd = m.compose.Update(msg)
	return m, cmd
}

// startDeploy deploys the decoder first, then the token pointing at it.
func (m model) startDeploy() (tea.Model, tea.Cmd) {
	name, kind := "ArithmeticDecoder", txDeployDecoder
	if m.cfg.Decoder != "" {
		name, kind = "ACToken", txDeployToken
	}
	code, err := readArtifactBytecode(filepath.Join(m.artifact, name+".bin"))
	if err != nil {
		m.err = fmt.Errorf("%w (run: make compile)", err)
		return m, nil
	}
	if kind == txDeployToken {
		code = chain.TokenInitCode(code, m.cfg.Decoder)
	}
	m.pending = &pendingTx{
		kind:    kind,
		to:      "",
		data:    code,
		summary: fmt.Sprintf("deploy %s (step %d/2, %d bytes)", name, int(kind)+1, len(code)),
	}
	return m.toConfirm()
}

func (m model) toConfirm() (tea.Model, tea.Cmd) {
	m.screen, m.busy = screenConfirm, true
	m.status, m.err = "estimating gas…", nil
	return m, m.prepareCmd(m.pending)
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.busy || m.pending == nil || m.pending.prep == nil {
		if msg.Type == tea.KeyEsc {
			m.pending, m.busy = nil, false
			m.screen = screenGallery
		}
		return m, nil
	}
	switch msg.String() {
	case "y", "Y":
		m.busy = true
		m.status = "signing and broadcasting…"
		return m, m.sendCmd(m.pending)
	case "n", "N", "esc", "q":
		m.pending = nil
		m.screen = screenGallery
		m.status = "cancelled"
	}
	return m, nil
}

func (m model) attemptUnlock() (tea.Model, tea.Cmd) {
	pass := m.pass.Value()
	if pass == "" {
		m.err = errors.New("passphrase required")
		return m, nil
	}
	var (
		w   *wallet.Key
		err error
	)
	if m.exists {
		w, err = wallet.Unlock(m.keystorePath, pass)
	} else {
		w, err = wallet.Create(m.keystorePath, pass)
	}
	if err != nil {
		m.err = err
		m.pass.Reset()
		return m, nil
	}
	m.wallet = w
	m.err = nil
	m.screen = screenGallery
	m.busy = true
	m.status = "loading…"
	return m, m.loadGalleryCmd()
}

// A cohesive neon/synthwave palette (truecolor).
var (
	cPink   = lipgloss.Color("#ff5fd2")
	cCyan   = lipgloss.Color("#5ef1ff")
	cPurple = lipgloss.Color("#a186ff")
	cDim    = lipgloss.Color("#6c7086")
	cGood   = lipgloss.Color("#4ade80")
	cBad    = lipgloss.Color("#ff5c7a")
	cInk    = lipgloss.Color("#12131a")
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(cPink)
	hintStyle   = lipgloss.NewStyle().Foreground(cDim)
	errStyle    = lipgloss.NewStyle().Foreground(cBad)
	okStyle     = lipgloss.NewStyle().Foreground(cGood)
	statusStyle = lipgloss.NewStyle().Foreground(cInk).Background(cPink).Padding(0, 1).Bold(true)
	emptyStyle  = lipgloss.NewStyle().Foreground(cDim).Italic(true)
	selStyle    = lipgloss.NewStyle().Foreground(cCyan).Bold(true)
	idStyle     = lipgloss.NewStyle().Foreground(cPurple)
	encStyle    = lipgloss.NewStyle().Foreground(cDim)
	glyphStyle  = lipgloss.NewStyle().Foreground(cCyan).Bold(true)
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cPurple).Padding(0, 2)
	headerStyle = lipgloss.NewStyle().Foreground(cInk).Background(cCyan).Bold(true).Padding(0, 1)
)

// costRamp colors a symbol's bit cost green (cheap) → red (expensive).
var costRamp = []lipgloss.Color{
	lipgloss.Color("#39d353"), lipgloss.Color("#86d34a"), lipgloss.Color("#c3d63f"), lipgloss.Color("#f2d338"),
	lipgloss.Color("#f2a838"), lipgloss.Color("#f27d3a"), lipgloss.Color("#f2523a"), lipgloss.Color("#ff2e2e"),
}

func costLevel(bits float64) int {
	lvl := int(bits)
	if lvl < 0 {
		lvl = 0
	}
	if lvl > 7 {
		lvl = 7
	}
	return lvl
}

func (m model) View() string {
	var body string
	switch m.screen {
	case screenUnlock:
		body = m.viewUnlock()
	case screenImport:
		body = m.viewImport()
	case screenGallery:
		body = m.viewGallery()
	case screenCompose:
		body = m.viewCompose()
	case screenReader:
		body = m.viewReader()
	case screenDecode:
		body = m.viewDecode()
	case screenSteps:
		body = m.viewSteps()
	case screenConfirm:
		body = m.viewConfirm()
	case screenSign:
		body = m.viewSign()
	case screenVoucher:
		body = m.viewVoucher()
	case screenSubmit:
		body = m.viewSubmit()
	}
	// Pre-wallet screens stay bare; everything else gets the app chrome.
	if m.screen == screenUnlock || m.screen == screenImport {
		return lipgloss.NewStyle().Padding(1, 2).Render(body)
	}
	return m.chrome(body)
}

// chrome: header banner, rounded panel, status bar.
func (m model) chrome(body string) string {
	header := headerStyle.Render(" ◆ AC-EVM ") +
		lipgloss.NewStyle().Foreground(cPurple).Render(" on-chain arithmetic coding ")
	return header + "\n" + panelStyle.Render(body) + "\n" + m.statusBar()
}

func (m model) statusBar() string {
	addr := "locked"
	if m.wallet != nil {
		addr = shortHex(m.wallet.Address().Hex())
	}
	nft := "no contract"
	if m.cfg.NFT != "" {
		nft = shortHex(m.cfg.NFT)
	}
	bar := statusStyle.Render(fmt.Sprintf(" ⬡ %s   ▸ %s   %d tokens ", addr, nft, len(m.tokens)))
	if m.busy {
		bar += "  " + m.spinner.View() + " " + okStyle.Render("working")
	}
	return bar
}

func shortHex(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "…" + s[len(s)-4:]
}

func (m model) viewUnlock() string {
	head := "Unlock your wallet"
	foot := "enter: continue   esc: quit"
	if !m.exists {
		head = "No wallet found — create one"
		foot = "enter: create new   ctrl+o: import an existing key   esc: quit"
	}
	b := titleStyle.Render("ac-evm") + "\n\n" + head + "\n" +
		hintStyle.Render(m.keystorePath) + "\n\n" + m.pass.View() + "\n"
	if m.err != nil {
		b += "\n" + errStyle.Render(m.err.Error()) + "\n"
	}
	return b + "\n" + hintStyle.Render(foot)
}

func (m model) viewImport() string {
	b := titleStyle.Render("import an existing key") + "\n\n" +
		hintStyle.Render("paste a private key hex; it is encrypted into "+m.keystorePath) + "\n\n" +
		m.importKey.View() + "\n" + m.pass.View() + "\n"
	if m.err != nil {
		b += "\n" + errStyle.Render(m.err.Error()) + "\n"
	}
	return b + "\n" + hintStyle.Render("tab: switch field   enter: import   esc: back")
}

func (m model) viewGallery() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("your on-chain messages") + "\n\n")
	if m.cfg.NFT == "" && m.cfg.Decoder != "" {
		b.WriteString(emptyStyle.Render("decoder deployed — press d to deploy the token") + "\n")
	} else if m.cfg.NFT == "" {
		b.WriteString(emptyStyle.Render("no contract yet — press d to deploy") + "\n")
	} else if len(m.tokens) == 0 {
		b.WriteString(emptyStyle.Render("you own no tokens — press m to mint a message") + "\n")
	} else {
		for i, t := range m.tokens {
			badge := fmt.Sprintf("%-9s", t.Badge)
			if i == m.cursor {
				b.WriteString(selStyle.Render(fmt.Sprintf("> #%-4d %s %s", t.ID, badge, t.First)) + "\n")
			} else {
				b.WriteString("  " + idStyle.Render(fmt.Sprintf("#%-4d", t.ID)) + " " +
					okStyle.Render(badge) + " " + t.First + "\n")
			}
		}
	}
	if m.err != nil {
		b.WriteString("\n" + errStyle.Render(m.err.Error()) + "\n")
	}
	if m.status != "" {
		b.WriteString("\n" + hintStyle.Render(m.status))
	}
	b.WriteString("\n" + hintStyle.Render("d: deploy   m: mint   v: submit voucher   enter: read   x: decode   r: refresh   q: quit"))
	return b.String()
}

func (m model) viewCompose() string {
	b := titleStyle.Render("mint a message") +
		hintStyle.Render(fmt.Sprintf("   %d/%d", len(m.compose.Value()), maxText)) + "\n\n" + m.compose.View() + "\n"
	if text := m.compose.Value(); text != "" {
		b += "\n" + m.encoderGraph(text) + "\n\n" + compressionGauge(text) + "\n"
	}
	if m.err != nil {
		b += "\n" + errStyle.Render(m.err.Error()) + "\n"
	}
	return b + "\n" + hintStyle.Render("enter: mint now (you pay gas)   ctrl+s: sign for later (free)   esc: cancel")
}

// compressionGauge: raw vs cold- vs warm-packed size, winner = PackBest's pick.
func compressionGauge(text string) string {
	src := []byte(text)
	raw := len(src)
	coldBlob, _ := ac.Pack(src, ac.PriorCold)
	warmBlob, _ := ac.Pack(src, ac.PriorEnglish)
	cold, warm := len(coldBlob), len(warmBlob)
	winner := "cold" // row label PackBest would pick
	if ac.PackBest(src)[0] == ac.PriorEnglish {
		winner = "warm"
	}

	maxv := raw
	if cold > maxv {
		maxv = cold
	}
	if warm > maxv {
		maxv = warm
	}

	const w = 28
	row := func(label string, n int, fill lipgloss.Style) string {
		blocks := 0
		if maxv > 0 {
			blocks = n * w / maxv
		}
		if blocks < 1 && n > 0 {
			blocks = 1
		}
		bar := fill.Render(strings.Repeat("█", blocks)) + hintStyle.Render(strings.Repeat("·", w-blocks))
		pct := "     "
		if label != "raw" && raw > 0 {
			d := (n - raw) * 100 / raw
			ps := fmt.Sprintf(" %+d%%", d)
			if d <= 0 {
				pct = okStyle.Render(ps)
			} else {
				pct = errStyle.Render(ps)
			}
		}
		badge := ""
		if label == winner {
			badge = "  " + selStyle.Render("◄ smallest")
		}
		return fmt.Sprintf("  %-4s %s %3dB%s%s", label, bar, n, pct, badge)
	}
	style := func(n int) lipgloss.Style {
		if n <= raw {
			return okStyle
		}
		return errStyle
	}
	return titleStyle.Render("compression (on-chain size)") + "\n" +
		row("raw", raw, hintStyle) + "\n" +
		row("cold", cold, style(cold)) + "\n" +
		row("warm", warm, style(warm))
}

// encoderGraph: per-character bit cost as colored sparklines (cold and warm).
func (m model) encoderGraph(text string) string {
	src := []byte(text)
	cold := ac.Analyze(src, nil)
	warm := ac.Analyze(src, ac.DefaultPrior)
	coldBytes := len(ac.Compress(src, nil))
	warmBytes := len(ac.Compress(src, ac.DefaultPrior))
	w := m.graphWidth()

	return hintStyle.Render("encoder cost per character  (green cheap · red surprising)") + "\n" +
		hintStyle.Render(fmt.Sprintf("  cold — %d chars → %d bytes on-chain", len(src), coldBytes)) + "\n" +
		"    " + sparkline(cold, w) + "\n" +
		hintStyle.Render(fmt.Sprintf("  warm — → ~%d bytes with a prior", warmBytes)) + "\n" +
		"    " + sparkline(warm, w)
}

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// graphWidth is the sparkline wrap width for the current window.
func (m model) graphWidth() int {
	w := m.width
	if w == 0 {
		w = 72 // before the first resize event
	}
	w -= 12
	if w < 16 {
		w = 16
	}
	return w
}

func sparkline(costs []ac.SymbolCost, width int) string {
	if width < 1 {
		width = 1
	}
	var b strings.Builder
	for i, c := range costs {
		if i > 0 && i%width == 0 {
			b.WriteString("\n    ")
		}
		lvl := costLevel(c.Bits)
		b.WriteString(lipgloss.NewStyle().Foreground(costRamp[lvl]).Render(string(sparkBlocks[lvl])))
	}
	return b.String()
}

func (m model) viewReader() string {
	id := uint64(0)
	if len(m.tokens) > 0 {
		id = m.tokens[m.cursor].ID
	}
	b := titleStyle.Render(fmt.Sprintf("token #%d", id)) + "\n" +
		hintStyle.Render("decoded on-chain via the contract's arithmetic decoder") + "\n\n"
	if len(m.readerEnc) > 0 {
		b += hintStyle.Render(fmt.Sprintf("stored on-chain (%d bytes, encoded):", len(m.readerEnc))) + "\n" +
			encStyle.Render("0x"+hex.EncodeToString(m.readerEnc)) + "\n\n"
		b += m.matchGraph(m.reader, m.readerEnc) + "\n\n"
	}
	b += hintStyle.Render("decoded:") + "\n" + m.reader + "\n\n" +
		hintStyle.Render("i: interval steps   d: decode these bytes yourself   q/esc: back")
	return b
}

func (m model) viewDecode() string {
	b := titleStyle.Render("decode a coded message") + "\n\n" +
		hintStyle.Render("paste arithmetic-coded hex (e.g. a token's stored bytes) and decode it locally") + "\n\n" +
		m.decodeIn.View() + "\n"
	if m.err != nil {
		b += "\n" + errStyle.Render(m.err.Error()) + "\n"
	} else if m.decodeOut != "" {
		b += "\n" + hintStyle.Render(fmt.Sprintf("decoded %d coded bytes → %d bytes:", len(m.decodeRaw), len(m.decodeOut))) + "\n" +
			m.decodeOut + "\n\n" + m.matchGraph(m.decodeOut, m.decodeRaw) + "\n"
	}
	if m.decodeOut != "" {
		b += hintStyle.Render("tab: step through the interval decode")
	}
	return b + "\n" + hintStyle.Render("enter: decode   esc: back")
}

const intervalWidth = 44

func (m model) stepsPerPage() int {
	n := m.height - 10
	if n < 6 {
		n = 6
	}
	return n
}

// viewSteps animates the decode: each symbol claims its slice of [0,1).
func (m model) viewSteps() string {
	costs := m.stepsCosts
	if len(costs) == 0 {
		return titleStyle.Render("nothing to decode") + "\n\n" + hintStyle.Render("esc: back")
	}
	pos := m.stepsPos
	if pos >= len(costs) {
		pos = len(costs) - 1
	}
	c := costs[pos]
	w := m.graphWidth()

	state := "⏸ paused"
	if m.stepsPlay {
		state = "▶ playing"
	}
	b := titleStyle.Render("watch it decode") +
		hintStyle.Render(fmt.Sprintf("   %s prior · %s", m.stepsPrior, state)) + "\n" +
		hintStyle.Render("each symbol claims its slice of [0,1); slice width = probability") + "\n\n"

	// decoded so far, materialising, cursor char highlighted
	b += hintStyle.Render("decoded:") + "\n" + wrapCursor(m.stepsText, pos, w) + "\n\n"

	// the current symbol's interval slice
	var bits float64
	for i := 0; i <= pos; i++ {
		bits += costs[i].Bits
	}
	b += fmt.Sprintf("step %d/%d  %s  %s\n", pos+1, len(costs),
		glyphStyle.Render(printable(c.Sym)), intervalBar(c.Lo, c.Hi, c.Bits))
	b += hintStyle.Render(fmt.Sprintf("        p=%.3f → %.1f bits   total %.0f bits ≈ %d bytes for %d chars",
		c.Hi-c.Lo, c.Bits, bits, int(bits+7)/8, pos+1)) + "\n\n"

	// history: the cost sparkline grows as the decode advances
	b += hintStyle.Render("cost so far:") + "\n    " + sparkline(costs[:pos+1], w)

	return b + "\n\n" + hintStyle.Render("space: play/pause   r: replay   ←→: step   esc: back")
}

// wrapCursor shows text[:pos+1] with pos highlighted, wrapped to width.
func wrapCursor(text string, pos, width int) string {
	src := []byte(text)
	if pos >= len(src) {
		pos = len(src) - 1
	}
	var b strings.Builder
	b.WriteString("  ")
	for i := 0; i <= pos; i++ {
		if i > 0 && i%width == 0 {
			b.WriteString("\n  ")
		}
		ch := string(rune(src[i]))
		if src[i] == '\n' {
			ch = "¶"
		}
		if i == pos {
			b.WriteString(selStyle.Render(ch))
		} else {
			b.WriteString(ch)
		}
	}
	return b.String()
}

// intervalBar draws [0,1) with the symbol's [lo,hi) slice highlighted.
func intervalBar(lo, hi, bits float64) string {
	a := int(lo * intervalWidth)
	z := int(hi*intervalWidth + 0.999) // round up so a sliver is still visible
	if z <= a {
		z = a + 1
	}
	if z > intervalWidth {
		z = intervalWidth
	}
	slice := lipgloss.NewStyle().Foreground(costRamp[costLevel(bits)])
	dot := lipgloss.NewStyle().Foreground(cDim)
	var sb strings.Builder
	for i := 0; i < intervalWidth; i++ {
		if i >= a && i < z {
			sb.WriteString(slice.Render("█"))
		} else {
			sb.WriteString(dot.Render("·"))
		}
	}
	return "[" + sb.String() + "]"
}

// printable renders a byte for the step list: the character, or an escape.
func printable(c byte) string {
	switch c {
	case ' ':
		return "' '"
	case '\n':
		return `\n`
	case '\t':
		return `\t`
	}
	if c < 0x20 || c > 0x7e {
		return fmt.Sprintf("x%02x", c)
	}
	return " " + string(rune(c)) + " "
}

// matchGraph puts the encoder's cost graph next to the real decoder's.
func (m model) matchGraph(decoded string, encoded []byte) string {
	payload, prior := encoded, []byte(nil)
	if id, out, err := ac.Unpack(encoded, 1<<16); err == nil && string(out) == decoded {
		payload = encoded[1:]
		prior, _ = ac.PriorByID(id)
	}
	enc := ac.Analyze([]byte(decoded), prior)
	dec, err := ac.AnalyzeDecode(payload, prior, 1<<16)
	if err != nil {
		return hintStyle.Render("(decode graph unavailable)")
	}
	w := m.graphWidth()
	line := hintStyle.Render("per-character cost — encode vs decode (identical = lockstep):") + "\n" +
		hintStyle.Render("  encode") + "\n    " + sparkline(enc, w) + "\n" +
		hintStyle.Render("  decode") + "\n    " + sparkline(dec, w)
	if graphsEqual(enc, dec) {
		line += "\n  " + okStyle.Render("✓ match — encoder and decoder walk the same model")
	} else {
		line += "\n  " + errStyle.Render("✗ graphs differ")
	}
	return line
}

func graphsEqual(a, b []ac.SymbolCost) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Sym != b[i].Sym || a[i].Bits != b[i].Bits {
			return false
		}
	}
	return true
}

func (m model) viewConfirm() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("confirm transaction") + "\n\n")
	if m.pending != nil {
		b.WriteString(m.pending.summary + "\n\n")
	}
	if m.pending == nil || m.pending.prep == nil {
		return b.String() + hintStyle.Render("estimating gas…") + "\n\n" + hintStyle.Render("esc: cancel")
	}
	p := m.pending.prep
	b.WriteString(fmt.Sprintf("  chain:     %d%s\n", p.ChainID, fujiNote(p.ChainID)))
	b.WriteString(fmt.Sprintf("  from:      %s (nonce %d)\n", m.wallet.Address().Hex(), p.Nonce))
	b.WriteString(fmt.Sprintf("  gas:       %d @ %s gwei\n", p.Gas, gwei(p.GasPrice)))
	b.WriteString(fmt.Sprintf("  max cost:  %s\n", weiToEther(p.MaxCost())))
	b.WriteString("\n" + okStyle.Render("this spends gas and is irreversible") + "\n")
	return b.String() + "\n" + hintStyle.Render("y: send   n/esc: cancel")
}

func readArtifactBytecode(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading artifact %s", path)
	}
	return hex.DecodeString(strings.TrimSpace(string(b)))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	const max = 60
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

func fujiNote(chainID uint64) string {
	if chainID == chain.FujiChainID {
		return " (Avalanche Fuji)"
	}
	return ""
}

func gwei(wei *big.Int) string {
	return new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e9)).Text('f', 2)
}

func weiToEther(wei *big.Int) string {
	return new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18)).Text('f', 6) + " AVAX"
}
