# Quickstart

Everything runs in Docker; `make` is the only entry point.

## 1. Build and verify

```bash
make compile   # solc → out/*.bin
make check     # Go↔EVM: decoder parity + EIP-712 mint, on a local anvil
make down
```

## 2. Try it locally (free, anvil)

```bash
make tui-local
```

No wallet yet → `ctrl+o` to import one of anvil's dev keys, e.g.
`ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80` (public,
local-only). State goes to `./state-local`.

Then: `d` deploy (two confirms) → `m` mint → `enter` read.

## 3. Fuji (real testnet)

**Wallet** — one of:

- New: `make cli ARGS=new`, then fund the printed address from the
  [Fuji faucet](https://core.app/tools/testnet-faucet/).
- Existing sealcast wallet (scrypt keystore → ac-evm format, key never printed):
  ```bash
  cd ../../ac-deaddrop/sealcast
  go run ./cmd/_migrate -in town/keystore.json -out ../../ac-evm-parent/ac-evm/state/keystore.json
  ```

**Deploy + mint:**

```bash
make tui        # d → deploy, m → mint, enter → read
```

or with the CLI:

```bash
make cli ARGS=deploy
make cli ARGS='mint -message "gm from fully on-chain"'
make cli ARGS='read -id 1'
```

## 4. Sign now, mint later

The author signs for free; anyone submits and pays gas; the token goes to the author.

- TUI: in compose, `ctrl+s` signs → voucher shown and saved to `state/vouchers/`.
  The submitter presses `v` in the gallery and pastes it.
- CLI:
  ```bash
  make cli ARGS='sign -message "signed now, minted later"'   # prints acevm:…
  make cli ARGS='submit -in /state/vouchers/nonce-0.txt'
  ```

## Good to know

- Max 1000 bytes of text per token (on-chain decode gas); the tools refuse more.
- Tokens are soulbound: they can't be transferred.
- On-chain content is public. Compression is not encryption.
- `./state` holds your keystore: keep it out of git.
- Every transaction shows gas and cost and waits for `y`.
