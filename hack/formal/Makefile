QUINT ?= npx --yes @informalsystems/quint@0.33.0
SAMPLES ?= 20000
STEPS ?= 80

.PHONY: check typecheck test simulate witnesses verify check-traces

check: typecheck test simulate witnesses ## Everything except Apalache.

typecheck:
	$(QUINT) typecheck promoter.qnt
	$(QUINT) typecheck promoter_test.qnt
	$(QUINT) typecheck tracecheck.qnt

test: ## Scripted runs, including the mergeSha counterexample, and the trace checker's own tests.
	$(QUINT) test promoter_test.qnt --main=stepsCurrent
	$(QUINT) test promoter_test.qnt --main=stepsReset
	$(QUINT) test tracecheck_test.qnt --main=tracecheckTest

simulate: ## Random runs of the models with RESET_MERGE_ON_NEW_SHA = true. Must find no violation.
	$(QUINT) run promoter.qnt --main=linearReset --invariant=safety --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=1
	$(QUINT) run promoter.qnt --main=diamondReset --invariant=safety --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=1

witnesses: ## Each witness must be violated, which shows the runs reach full promotions and no-ops.
	! $(QUINT) run promoter.qnt --main=linearReset --invariant=witnessLastEnvGetsNewestDry --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=0
	! $(QUINT) run promoter.qnt --main=diamondReset --invariant=witnessLastEnvGetsNewestDry --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=0
	! $(QUINT) run promoter.qnt --main=linearReset --invariant=witnessNoopPassThrough --max-samples=$(SAMPLES) --max-steps=$(STEPS) --verbosity=0

verify: ## Bounded model checking with Apalache (needs Java 17+). 4 steps take under a minute; 8 did not finish in 30.
	$(QUINT) verify promoter.qnt --main=linearReset --invariant=safety --max-steps=$(or $(VERIFY_STEPS),4)

check-traces: ## Check runs recorded with PROMOTER_TRACE_DIR against the model: make check-traces TRACE_DIR=/abs/dir
	@test -n "$(TRACE_DIR)" || { echo "set TRACE_DIR to the directory PROMOTER_TRACE_DIR pointed at"; exit 2; }
	cd ../.. && QUINT="$(QUINT)" go run ./hack/formal/tracecheck -formal hack/formal $(abspath $(TRACE_DIR))
