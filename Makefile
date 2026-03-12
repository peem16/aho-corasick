BENCH2JSON := go run ./tools/bench2json/
PKG        := ./...
BENCHFLAGS := -count=1 -timeout=300s

# ─── Default ──────────────────────────────────────────────────────────────────

.DEFAULT_GOAL := help

# ─── Development ──────────────────────────────────────────────────────────────

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

.PHONY: test
test: ## Run all tests with race detector
	go test -race -count=1 $(PKG)

.PHONY: vet
vet: ## Run go vet
	go vet $(PKG)

.PHONY: check
check: vet test ## Run vet + tests (pre-commit gate)

# ─── Benchmarks: capture ──────────────────────────────────────────────────────

.PHONY: bench
bench: ## Run full benchmark suite once → results/*.json
	go test $(BENCHFLAGS) -bench=. -benchmem $(PKG) | $(BENCH2JSON)

.PHONY: bench-overlapping
bench-overlapping: ## Run only FindOverlapping benchmarks → results/overlapping.json
	go test $(BENCHFLAGS) -bench=BenchmarkFindOverlapping -benchmem $(PKG) | $(BENCH2JSON)

.PHONY: bench-scaling
bench-scaling: ## Run match-density scaling benchmarks → results/scaling.json
	go test $(BENCHFLAGS) -bench=BenchmarkScaling -benchmem $(PKG) | $(BENCH2JSON)

.PHONY: bench-all
bench-all: ## Run full suite 3× for statistical reliability → results/*.json
	go test $(BENCHFLAGS) -bench=. -benchmem -count=3 $(PKG) | $(BENCH2JSON)

.PHONY: bench-view
bench-view: ## Print a summary table of the last captured run
	$(BENCH2JSON) --view

.PHONY: bench-clean
bench-clean: ## Delete all captured benchmark results (results/*.json)
	rm -rf results/

.PHONY: bench-history-clear
bench-history-clear: ## Reset scripts/history.json to empty (keeps schema, removes all runs)
	$(BENCH2JSON) --clear-history

# ─── Benchmarks: history + charts ─────────────────────────────────────────────

.PHONY: bench-history
bench-history: ## Build scripts/history.json from results/overlapping.json
	$(BENCH2JSON) --gen-history

.PHONY: bench-plot
bench-plot: ## Generate SVG charts from scripts/history.json (requires node)
	node scripts/plot.js

.PHONY: bench-report
bench-report: bench bench-history bench-plot ## Full pipeline: capture → history → SVG charts
