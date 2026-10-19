ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

.PHONY: red green

## TDD guard (CLAUDE.md §1) -----------------------------------------------

red: ## Record a failing test: make red PKG=./internal/x/core RUN='^TestName$$'
	@$(ROOT)/scripts/tdd.sh red '$(value PKG)' '$(value RUN)'

green: ## Rerun the recorded test + its package with -race; clear .tdd/red when green
	@$(ROOT)/scripts/tdd.sh green
