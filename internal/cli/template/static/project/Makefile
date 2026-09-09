# Scorix Project Makefile
#
# Install scorix CLI first:
#   go install github.com/tradalab/scorix/cmd/scorix@latest
SCORIX_CLI := scorix

gen:
	$(SCORIX_CLI) generate proto -d .
	$(SCORIX_CLI) generate model -d .

# Force-regenerate everything. Destroys custom edits in *_model.go and logic
# files - only use after committing local changes.
gen-force:
	$(SCORIX_CLI) generate proto -d . --force
	$(SCORIX_CLI) generate model -d . --force

dev:
	$(SCORIX_CLI) dev

shell-build:
	cd shell && pnpm build

shell-dev:
	cd shell && pnpm dev

run:
	go run . -mode=desktop -assets=shell/dist

run-web:
	go run . -mode=web -assets=shell/dist

build:
	cd shell && pnpm build
	go build -o dist/{{ .Name }} .

# Both halves, and neither may write. `pnpm lint` is `tsc --noEmit` by default;
# widen it in shell/package.json if you add eslint. A gate that edits files
# (eslint --fix) reports green on code that was never committed.
lint:
	$(SCORIX_CLI) lint
	cd shell && pnpm lint

test:
	$(SCORIX_CLI) test

.PHONY: gen gen-force dev shell-build shell-dev run run-web build lint test
