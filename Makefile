# Project Makefile. Framework targets come from Makefile.servicepack; the
# targets below are this project's own.

include Makefile.servicepack

.PHONY: test-api

# The framework's `test` and `test-integration` do not pass
# -tags=integration, so the testcontainers suite under tests/ is compiled out
# of them; only `test-coverage` sets the tag. This target runs that suite on
# its own: it builds the production image and drives real monerod and
# monero-wallet-rpc on regtest.
test-api: dev-image ## Run the production-image API suite on regtest (testcontainers)
	@$(DEV_RUN_DIND) go test -race -count=1 -timeout=1800s \
		-tags=integration ./tests/...

COMPOSE_FILE ?= docker-compose.yml
COMPOSE_ENV_FILE ?= .env.example
COMPOSE_RENDERED := build/compose-rendered.json

.PHONY: audit-compose

# Renders the compose file on the host, because `docker compose` is a host
# tool, then runs the assertions in the development image. The rendered
# document is what gets audited, so variable substitution is resolved first.
# The compose file reads .env; a temporary copy of .env.example stands in
# when no .env exists and is removed afterwards.
audit-compose: dev-image ## Audit the production compose file against the hardened floor
	@mkdir -p "$(dir $(COMPOSE_RENDERED))"
	@created_env=0; \
	if [ ! -f .env ]; then cp "$(COMPOSE_ENV_FILE)" .env; created_env=1; fi; \
	docker compose --env-file "$(COMPOSE_ENV_FILE)" -f "$(COMPOSE_FILE)" \
		config --format json >"$(COMPOSE_RENDERED)"; status=$$?; \
	if [ "$$created_env" = 1 ]; then rm -f .env; fi; \
	exit $$status
	@$(DEV_RUN) bash scripts/audit-compose-floor.sh "$(COMPOSE_RENDERED)"
	@rm -f "$(COMPOSE_RENDERED)"
