QUINT ?= npx --yes @informalsystems/quint@0.33.0
SAMPLES ?= 20000
STEPS ?= 80

.PHONY: check typecheck test simulate witnesses verify

check: typecheck test simulate witnesses ## Everything except Apalache.

typecheck:
	$(QUINT) typecheck promoter.qnt
	$(QUINT) typecheck promoter_test.qnt

test: ## Scripted runs, including the mergeSha counterexample.
	$(QUINT) test promoter_test.qnt --main=stepsCurrent
	$(QUINT) test promoter_test.qnt --main=stepsReset

simulate: ## Random runs of the models with RESET_MERGE_ON_NEW_SHA = true. Must find no violation.
	$(QUINT) run promoter.qnt --main=linearReset --invariant=safety --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=1
	$(QUINT) run promoter.qnt --main=diamondReset --invariant=safety --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=1

witnesses: ## Each witness must be violated, which shows the runs reach full promotions and no-ops.
	! $(QUINT) run promoter.qnt --main=linearReset --invariant=witnessLastEnvGetsNewestDry --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=0
	! $(QUINT) run promoter.qnt --main=diamondReset --invariant=witnessLastEnvGetsNewestDry --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=0
	! $(QUINT) run promoter.qnt --main=linearReset --invariant=witnessNoopPassThrough --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=0

verify: ## Bounded model checking with Apalache (needs Java 17+; slow).
	$(QUINT) verify promoter.qnt --main=linearReset --invariant=safety --max-steps=$(or $(VERIFY_STEPS),8)
