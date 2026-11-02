DC := docker compose

.PHONY: compile chain test check tui tui-local cli down

compile:
	$(DC) run --rm solc

chain:
	$(DC) up -d anvil

test:
	$(DC) run --rm test

check: compile chain
	$(DC) run --rm check

# TUI against Fuji (or RPC=url).
tui: compile
	$(DC) build tui
	$(DC) run --rm tui $(if $(RPC),-rpc $(RPC))

# TUI against the local anvil, with its own state dir.
tui-local: compile chain
	$(DC) build tui
	$(DC) run --rm -v ./state-local:/state tui -rpc http://anvil:8545

# CLI in the same image: make cli ARGS='mint -message "gm"'
cli: compile
	$(DC) build tui
	$(DC) run --rm --no-deps cli $(ARGS)

down:
	$(DC) down
