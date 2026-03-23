CMDS := slaude slacp slagent-demo claude-command-classifier-hook

.PHONY: help build $(CMDS) vet clean openclaw-sandbox

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

build: $(CMDS) ## Build all binaries

$(CMDS): ## Build a single binary (e.g. make slaude)
	go build -o $@ ./cmd/$@/

vet: ## Run go vet
	go vet ./...

openclaw-sandbox: slacp ## Run OpenClaw + slacp in isolated sandbox (API key from Keychain)
	./sandbox/openclaw-slacp.sh

clean: ## Remove built binaries
	rm -f $(CMDS)
