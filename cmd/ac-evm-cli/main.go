// Command ac-evm-cli is the scriptable twin of the ac-evm TUI (same state dir).
//
//	ac-evm-cli new                       create a wallet keystore to fund
//	ac-evm-cli deploy                    deploy ArithmeticDecoder + ACToken
//	ac-evm-cli mint -message "gm"        compress + mint on-chain
//	ac-evm-cli read -id 1                decode it on-chain
//	ac-evm-cli sign -message "gm"        sign a mint voucher (no gas)
//	ac-evm-cli submit -voucher acevm:…   mint someone's voucher (you pay gas)
package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smhaaker/ac-evm/internal/ac"
	"github.com/smhaaker/ac-evm/internal/chain"
	"github.com/smhaaker/ac-evm/internal/config"
	"github.com/smhaaker/ac-evm/internal/eth"
	"github.com/smhaaker/ac-evm/internal/txn"
	"github.com/smhaaker/ac-evm/internal/wallet"
)

const usage = `ac-evm-cli - arithmetic coding, decoded in the EVM

Commands:
  new      create a fresh wallet keystore and print its address to fund
  import   import an existing private key into an encrypted keystore
  deploy   deploy ArithmeticDecoder + ACToken (default Avalanche Fuji)
  mint     compress a message and mint it as a fully on-chain token
  read     decode a token on-chain (view call, free)
  sign     sign a mint voucher for later (no gas)
  submit   mint a signed voucher to its author (you pay gas)

Run "ac-evm-cli <command> -h" for flags.
`

// maxText mirrors MAX_OUT in ACToken.sol.
const maxText = 1000

const voucherTTL = 30 * 24 * time.Hour

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "deploy":
		err = cmdDeploy(os.Args[2:])
	case "new":
		err = cmdNew(os.Args[2:])
	case "import":
		err = cmdImport(os.Args[2:])
	case "mint":
		err = cmdMint(os.Args[2:])
	case "read":
		err = cmdRead(os.Args[2:])
	case "sign":
		err = cmdSign(os.Args[2:])
	case "submit":
		err = cmdSubmit(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "ac-evm-cli: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ac-evm-cli:", err)
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

func readInput(path string) ([]byte, error) {
	if path == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
		return b, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return b, nil
}

// cmdDeploy deploys the decoder, then the token pointing at it; each behind a confirm.
func cmdDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory holding keystore.json")
	rpc := fs.String("rpc", chain.FujiRPC, "JSON-RPC endpoint")
	out := fs.String("out", "out", "directory of compiled .bin files (make compile)")
	passfile := fs.String("passfile", "", "file holding the keystore passphrase (else $ACEVM_PASSPHRASE or prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*dir)
	if err != nil {
		return err
	}
	if cfg.NFT != "" {
		return fmt.Errorf("deploy: %s already has a token at %s", config.Path(*dir), cfg.NFT)
	}
	stdin := bufio.NewReader(os.Stdin)
	pass, err := readPassphrase(*passfile, stdin)
	if err != nil {
		return err
	}
	w, err := wallet.Unlock(filepath.Join(*dir, "keystore.json"), pass)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	c := chain.Dial(*rpc)
	cfg.RPC = *rpc

	if cfg.Decoder == "" {
		addr, err := deployOne(ctx, c, w, stdin, *out, "ArithmeticDecoder", "", 1)
		if err != nil {
			return err
		}
		cfg.Decoder = addr
		if err := cfg.Save(*dir); err != nil {
			return err
		}
	}
	addr, err := deployOne(ctx, c, w, stdin, *out, "ACToken", cfg.Decoder, 2)
	if err != nil {
		return err
	}
	cfg.NFT = addr
	if err := cfg.Save(*dir); err != nil {
		return err
	}
	fmt.Printf("saved to %s -- now mint: ac-evm-cli mint -message \"…\"\n", config.Path(*dir))
	return nil
}

func deployOne(ctx context.Context, c *chain.Client, w *wallet.Key, stdin *bufio.Reader, out, name, decoder string, step int) (string, error) {
	code, err := loadBytecode(filepath.Join(out, name+".bin"))
	if err != nil {
		return "", err
	}
	if decoder != "" {
		code = chain.TokenInitCode(code, decoder)
	}
	from := w.Address().Hex()
	prep, err := txn.Prepare(ctx, c, from, "", code)
	if err != nil {
		return "", fmt.Errorf("preparing deploy (endpoint reachable / wallet funded?): %w", err)
	}

	fmt.Printf("Deploy %s (step %d/2)\n", name, step)
	fmt.Printf("  chain id:  %d%s\n", prep.ChainID, fujiNote(prep.ChainID))
	fmt.Printf("  from:      %s  (nonce %d)\n", from, prep.Nonce)
	fmt.Printf("  bytecode:  %d bytes\n", len(code))
	fmt.Printf("  gas:       %d @ %s gwei\n", prep.Gas, gwei(prep.GasPrice))
	fmt.Printf("  max cost:  %s\n", weiToEther(prep.MaxCost()))
	if !confirm(stdin, "Send this transaction? [y/N] ") {
		return "", errors.New("deploy: cancelled")
	}
	fmt.Printf("\nsigning and broadcasting; waiting for it to be mined...\n")

	rcpt, sent, err := txn.Send(ctx, c, w, prep)
	if err != nil {
		return "", fmt.Errorf("deploying (tx %s): %w", sent, err)
	}
	if rcpt.Status != 1 {
		return "", fmt.Errorf("deploy transaction reverted (tx %s)", sent)
	}
	fmt.Printf("deployed %s at %s in block %d (gas used %d)\n\n", name, rcpt.ContractAddress, rcpt.BlockNumber, rcpt.GasUsed)
	return rcpt.ContractAddress, nil
}

// cmdNew creates a fresh wallet keystore and prints its address to fund.
func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory for the keystore")
	passfile := fs.String("passfile", "", "file with the new keystore passphrase (else $ACEVM_PASSPHRASE or prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	ksPath := filepath.Join(*dir, "keystore.json")
	if _, err := os.Stat(ksPath); err == nil {
		return fmt.Errorf("new: %s already exists; use a different -dir or remove it", ksPath)
	}
	pass, err := readPassphrase(*passfile, bufio.NewReader(os.Stdin))
	if err != nil {
		return err
	}
	w, err := wallet.Create(ksPath, pass)
	if err != nil {
		return err
	}
	fmt.Printf("new wallet: %s\n", w.Address().Hex())
	fmt.Printf("keystore:   %s\n\n", ksPath)
	fmt.Printf("fund the wallet address above with a little Fuji AVAX, then:\n")
	fmt.Printf("  ac-evm-cli deploy -dir %s\n", *dir)
	return nil
}

// cmdImport encrypts an existing private key into a keystore. The key comes
// from a file or stdin, never a flag (shell history).
func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory for the keystore")
	keyfile := fs.String("keyfile", "", "file containing the raw private key hex (else read from stdin)")
	passfile := fs.String("passfile", "", "file with the new keystore passphrase (else $ACEVM_PASSPHRASE or prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	ksPath := filepath.Join(*dir, "keystore.json")
	if _, err := os.Stat(ksPath); err == nil {
		return fmt.Errorf("import: %s already exists; remove it first to replace it", ksPath)
	}

	stdin := bufio.NewReader(os.Stdin)
	var privHex string
	if *keyfile != "" {
		b, err := os.ReadFile(*keyfile)
		if err != nil {
			return fmt.Errorf("import: reading keyfile: %w", err)
		}
		privHex = strings.TrimSpace(string(b))
	} else {
		fmt.Fprint(os.Stderr, "private key hex (visible): ")
		line, err := stdin.ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("import: reading private key: %w", err)
		}
		privHex = strings.TrimSpace(line)
	}

	pass, err := readPassphrase(*passfile, stdin)
	if err != nil {
		return err
	}
	w, err := wallet.Import(ksPath, privHex, pass)
	if err != nil {
		return err
	}
	fmt.Printf("imported wallet %s\n", w.Address().Hex())
	fmt.Printf("keystore written to %s\n", ksPath)
	return nil
}

// nftTarget resolves the RPC endpoint and ACToken address from config, with
// optional flag overrides.
func nftTarget(dir, rpcFlag, nftFlag string) (rpc, nft string, err error) {
	cfg, err := config.Load(dir)
	if err != nil {
		return "", "", err
	}
	rpc, nft = cfg.RPC, cfg.NFT
	if rpcFlag != "" {
		rpc = rpcFlag
	}
	if nftFlag != "" {
		nft = nftFlag
	}
	if nft == "" {
		return "", "", errors.New("no ACToken configured; run `ac-evm-cli deploy` or pass -nft")
	}
	return rpc, nft, nil
}

// message reads -message or -in and enforces the on-chain cap.
func message(text, in string) ([]byte, error) {
	msg := []byte(text)
	if in != "" {
		var err error
		if msg, err = readInput(in); err != nil {
			return nil, err
		}
	}
	if len(msg) == 0 {
		return nil, errors.New("message is empty (use -message or -in)")
	}
	if len(msg) > maxText {
		return nil, fmt.Errorf("message is %d bytes; the on-chain cap is %d", len(msg), maxText)
	}
	return msg, nil
}

func unlock(dir, passfile string, stdin *bufio.Reader) (*wallet.Key, error) {
	pass, err := readPassphrase(passfile, stdin)
	if err != nil {
		return nil, err
	}
	return wallet.Unlock(filepath.Join(dir, "keystore.json"), pass)
}

// cmdMint compresses a message with the best prior and mints it.
func cmdMint(args []string) error {
	fs := flag.NewFlagSet("mint", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	text := fs.String("message", "", "message text to mint")
	in := fs.String("in", "", "read the message from a file, or - for stdin")
	rpc := fs.String("rpc", "", "JSON-RPC endpoint (default: config)")
	nftFlag := fs.String("nft", "", "ACToken address (default: config)")
	passfile := fs.String("passfile", "", "keystore passphrase file (else $ACEVM_PASSPHRASE or prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rpcURL, nft, err := nftTarget(*dir, *rpc, *nftFlag)
	if err != nil {
		return err
	}
	msg, err := message(*text, *in)
	if err != nil {
		return err
	}
	compressed := ac.PackBest(msg)

	stdin := bufio.NewReader(os.Stdin)
	w, err := unlock(*dir, *passfile, stdin)
	if err != nil {
		return err
	}
	return sendMint(rpcURL, nft, w, stdin, chain.MintCalldata(compressed), func() {
		fmt.Printf("Mint on-chain message\n")
		fmt.Printf("  message:   %d bytes -> %d packed (%s prior)\n", len(msg), len(compressed), ac.PriorName(compressed[0]))
	}, *dir)
}

// sendMint prepares, shows, confirms, and sends a mint-style tx to the token.
func sendMint(rpcURL, nft string, w *wallet.Key, stdin *bufio.Reader, data []byte, header func(), dir string) error {
	c, err := chain.New(rpcURL, nft)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	prep, err := txn.Prepare(ctx, c, w.Address().Hex(), nft, data)
	if err != nil {
		return fmt.Errorf("preparing mint: %w", chain.ExplainRevert(err))
	}
	header()
	fmt.Printf("  nft:       %s (chain %d%s)\n", nft, prep.ChainID, fujiNote(prep.ChainID))
	fmt.Printf("  gas:       %d @ %s gwei\n", prep.Gas, gwei(prep.GasPrice))
	fmt.Printf("  max cost:  %s\n", weiToEther(prep.MaxCost()))
	if !confirm(stdin, "Send this transaction? [y/N] ") {
		return errors.New("mint: cancelled")
	}
	fmt.Printf("\nsigning and broadcasting; waiting for it to be mined...\n")

	rcpt, sent, err := txn.Send(ctx, c, w, prep)
	if err != nil {
		return fmt.Errorf("minting (tx %s): %w", sent, err)
	}
	if rcpt.Status != 1 {
		return fmt.Errorf("mint transaction reverted (tx %s)", sent)
	}
	id, err := c.TotalSupply(ctx)
	if err != nil {
		id = 0
	}
	fmt.Printf("\nminted token #%d in block %d (tx %s)\n", id, rcpt.BlockNumber, sent)
	fmt.Printf("read it back on-chain:  ac-evm-cli read -dir %s -id %d\n", dir, id)
	return nil
}

// cmdRead decodes a token via the contract's read() view call.
func cmdRead(args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	id := fs.Uint64("id", 0, "token id to read (required)")
	rpc := fs.String("rpc", "", "JSON-RPC endpoint (default: config)")
	nftFlag := fs.String("nft", "", "ACToken address (default: config)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == 0 {
		return errors.New("read: -id is required")
	}
	rpcURL, nft, err := nftTarget(*dir, *rpc, *nftFlag)
	if err != nil {
		return err
	}
	c, err := chain.New(rpcURL, nft)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	owner, err := c.OwnerOf(ctx, *id)
	if err != nil {
		return fmt.Errorf("read: token #%d not found: %w", *id, err)
	}
	encoded, err := c.CompressedOf(ctx, *id)
	if err != nil {
		return fmt.Errorf("read: fetching stored bytes for token #%d: %w", *id, err)
	}
	msg, err := c.ReadMessage(ctx, *id)
	if err != nil {
		return fmt.Errorf("read: decoding token #%d: %w", *id, err)
	}
	fmt.Printf("token #%d  owner %s\n", *id, owner)
	fmt.Printf("encoded on-chain (%d bytes): 0x%s\n", len(encoded), hex.EncodeToString(encoded))
	if id, out, err := ac.Unpack(encoded, 1<<16); err == nil && string(out) == msg {
		fmt.Printf("prior: %s\n", ac.PriorName(id))
	}
	fmt.Printf("decoded on-chain (%d bytes):\n%s\n", len(msg), msg)
	return nil
}

// cmdSign signs a mint voucher (EIP-712); no transaction, no gas.
func cmdSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	text := fs.String("message", "", "message text to sign")
	in := fs.String("in", "", "read the message from a file, or - for stdin")
	rpc := fs.String("rpc", "", "JSON-RPC endpoint (default: config)")
	nftFlag := fs.String("nft", "", "ACToken address (default: config)")
	passfile := fs.String("passfile", "", "keystore passphrase file (else $ACEVM_PASSPHRASE or prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rpcURL, nft, err := nftTarget(*dir, *rpc, *nftFlag)
	if err != nil {
		return err
	}
	msg, err := message(*text, *in)
	if err != nil {
		return err
	}
	blob := ac.PackBest(msg)

	stdin := bufio.NewReader(os.Stdin)
	w, err := unlock(*dir, *passfile, stdin)
	if err != nil {
		return err
	}
	c, err := chain.New(rpcURL, nft)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	chainID, err := c.ChainID(ctx)
	if err != nil {
		return err
	}
	nonce, err := c.Nonces(ctx, w.Address().Hex())
	if err != nil {
		return err
	}
	deadline := uint64(time.Now().Add(voucherTTL).Unix())

	fmt.Fprintf(os.Stderr, "Sign mint voucher\n")
	fmt.Fprintf(os.Stderr, "  message:   %d bytes -> %d packed (%s prior)\n", len(msg), len(blob), ac.PriorName(blob[0]))
	fmt.Fprintf(os.Stderr, "  nft:       %s (chain %d%s)\n", nft, chainID, fujiNote(chainID))
	fmt.Fprintf(os.Stderr, "  nonce:     %d (submit this voucher before signing another)\n", nonce)
	fmt.Fprintf(os.Stderr, "  expires:   %s\n", time.Unix(int64(deadline), 0).Format("2006-01-02 15:04"))
	if !confirm(stdin, "Sign? No gas; anyone can submit it and the token goes to you. [y/N] ") {
		return errors.New("sign: cancelled")
	}
	tok, err := eth.ParseAddress(nft)
	if err != nil {
		return err
	}
	sig, err := w.Sign(eth.MintDigest(chainID, tok, w.Address(), blob, nonce, deadline))
	if err != nil {
		return err
	}
	v := eth.Voucher{Author: w.Address(), Deadline: deadline, Sig: sig, Blob: blob}.String()
	path := filepath.Join(*dir, "vouchers", fmt.Sprintf("nonce-%d.txt", nonce))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(v+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "saved to %s\n", path)
	fmt.Println(v)
	return nil
}

// cmdSubmit mints a voucher to its author; the submitter pays gas.
func cmdSubmit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	voucher := fs.String("voucher", "", "acevm: voucher string")
	in := fs.String("in", "", "read the voucher from a file, or - for stdin")
	rpc := fs.String("rpc", "", "JSON-RPC endpoint (default: config)")
	nftFlag := fs.String("nft", "", "ACToken address (default: config)")
	passfile := fs.String("passfile", "", "keystore passphrase file (else $ACEVM_PASSPHRASE or prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rpcURL, nft, err := nftTarget(*dir, *rpc, *nftFlag)
	if err != nil {
		return err
	}
	s := *voucher
	if *in != "" {
		b, err := readInput(*in)
		if err != nil {
			return err
		}
		s = string(b)
	}
	v, err := eth.ParseVoucher(s)
	if err != nil {
		return err
	}
	_, text, err := ac.Unpack(v.Blob, maxText)
	if err != nil {
		return fmt.Errorf("submit: voucher text does not decode: %w", err)
	}
	if time.Now().Unix() > int64(v.Deadline) {
		return errors.New("submit: voucher has expired")
	}

	stdin := bufio.NewReader(os.Stdin)
	w, err := unlock(*dir, *passfile, stdin)
	if err != nil {
		return err
	}
	return sendMint(rpcURL, nft, w, stdin, chain.MintWithSigCalldata(v.Author.Hex(), v.Blob, v.Deadline, v.Sig[:]), func() {
		fmt.Printf("Submit voucher (you pay gas; the token goes to the author)\n")
		fmt.Printf("  author:    %s\n", v.Author.Hex())
		fmt.Printf("  message:   %d bytes -> %d packed (%s prior)\n", len(text), len(v.Blob), ac.PriorName(v.Blob[0]))
		fmt.Printf("  decoded:   %s\n", text)
	}, *dir)
}

func loadBytecode(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s (run: make compile): %w", path, err)
	}
	code, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("bytecode is not hex: %w", err)
	}
	if len(code) == 0 {
		return nil, errors.New("bytecode is empty")
	}
	return code, nil
}

// readPassphrase: -passfile, then $ACEVM_PASSPHRASE, then an (echoing) prompt.
func readPassphrase(passfile string, in *bufio.Reader) (string, error) {
	if passfile != "" {
		b, err := os.ReadFile(passfile)
		if err != nil {
			return "", fmt.Errorf("reading passfile: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if v := os.Getenv("ACEVM_PASSPHRASE"); v != "" {
		return v, nil
	}
	fmt.Fprint(os.Stderr, "keystore passphrase (visible): ")
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("reading passphrase: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func confirm(in *bufio.Reader, prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	line, _ := in.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func fujiNote(chainID uint64) string {
	if chainID == chain.FujiChainID {
		return " (Avalanche Fuji)"
	}
	return ""
}

func gwei(wei *big.Int) string {
	f := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e9))
	return f.Text('f', 2)
}

func weiToEther(wei *big.Int) string {
	f := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18))
	return f.Text('f', 6) + " AVAX"
}
