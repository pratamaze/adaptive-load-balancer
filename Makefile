ifneq ("$(wildcard Makefile.local)","")
include Makefile.local
endif

LB_MODULE_DIR ?= $(CURDIR)/load-balancer

REMOTE_USER ?= ripe
REMOTE_HOST ?= 127.0.0.1
SSH_KEY ?= ~/.ssh/id_rsa
SSH_PORT ?= 22

REMOTE_BASE_DIR ?= /home/ripe/load-balancer
LB_STORAGE_DIR ?= $(REMOTE_BASE_DIR)/storage
LB_CONFIGS_DIR ?= $(REMOTE_BASE_DIR)/configs

LOCAL_DATASET ?= $(LB_MODULE_DIR)/storage/fuzzy_training_data_clean.csv
LOCAL_BASE_PARAMS ?= $(LB_MODULE_DIR)/configs/base_fuzzy_params.json
LOCAL_OPT_PARAMS ?= $(LB_MODULE_DIR)/storage/optimized_fuzzy_params.json
LOCAL_TRAIN_REPORT ?= $(LB_MODULE_DIR)/storage/mopso_offline_report.json

REMOTE_DATASET ?= $(LB_STORAGE_DIR)/fuzzy_training_data.csv
REMOTE_OPT_PARAMS ?= $(LB_CONFIGS_DIR)/optimized_fuzzy_params.json

LB_SERVICE ?= fmopso-stack_entry-point
NODE1_SERVICE ?= fmopso-stack_api-node1
NODE2_SERVICE ?= fmopso-stack_api-node2
LB_MODE_LABEL_KEY ?= fmopso.mode

MOPSO_PARTICLES ?= 50
MOPSO_ITERATIONS ?= 3000
MOPSO_SPREAD ?= 8
MOPSO_SEED ?= 0
MOPSO_RUNS ?= 5
MOPSO_ALLOW_REGRESSION ?= false
DATASET_LOG_SINCE ?= 20m

DATASET_HEADER := timestamp_utc,window_ms,traffic_log_mode,cpu_usage_unit,node1_name,node2_name,node1_requests,node2_requests,total_requests,node1_cpu_raw_usage,node2_cpu_raw_usage,node1_cpu_normalized_usage,node2_cpu_normalized_usage,node1_cpu_capacity,node2_cpu_capacity,node1_queue,node2_queue,node1_response_ms,node2_response_ms,node1_fuzzy_score,node2_fuzzy_score,os_idle_cpu

SSH_OPTS := -i $(SSH_KEY) -p $(SSH_PORT)
SCP_OPTS := -i $(SSH_KEY) -P $(SSH_PORT)
REMOTE_ADDR := $(REMOTE_USER)@$(REMOTE_HOST)

.PHONY: help dataset-pull dataset-capture-live train-mopso params-push offline-flow params-pull dataset-reset-local dataset-reset-remote reset-swarm prep-fuzzy prep-mopso show-lb-source verify-fuzzy verify-mopso logs-lb-source

help:
	@echo "Available targets:"
	@echo "  make dataset-pull     # Snapshot dataset CSV dari stdout docker service logs"
	@echo "  make dataset-capture-live # Capture dataset live dari stdout docker service logs (Ctrl+C untuk stop)"
	@echo "  make train-mopso      # Training MOPSO offline di laptop"
	@echo "  make params-push      # Push parameter optimized ke VPS (configs/optimized_fuzzy_params.json)"
	@echo "  make offline-flow     # Jalankan pull -> train -> push"
	@echo "  make params-pull      # Ambil parameter optimized dari VPS"
	@echo "  make dataset-reset-local"
	@echo "  make dataset-reset-remote"
	@echo "  make logs-lb-source    # Tampilkan marker source parameter dari log entrypoint"

dataset-pull:
	@mkdir -p $(dir $(LOCAL_DATASET))
	ssh $(SSH_OPTS) $(REMOTE_ADDR) 'docker service logs --raw --since $(DATASET_LOG_SINCE) $(LB_SERVICE) 2>&1' | \
	grep '\[DATASET_CSV\]' | \
	awk -v header='$(DATASET_HEADER)' '\
		BEGIN { print header } \
		{ \
			line=$$0; \
			sub(/^.*\[DATASET_CSV\][[:space:]]*/, "", line); \
			if (line == "" || line == header) next; \
			print line; \
		}' > $(LOCAL_DATASET)
	@echo "Dataset captured from stdout logs: $(LOCAL_DATASET)"

dataset-capture-live:
	@mkdir -p $(dir $(LOCAL_DATASET))
	@echo "Capturing live dataset logs to $(LOCAL_DATASET) (Ctrl+C untuk stop)..."
	ssh $(SSH_OPTS) $(REMOTE_ADDR) 'docker service logs --raw -f --since 0s $(LB_SERVICE) 2>&1' | \
	grep '\[DATASET_CSV\]' | \
	awk -v header='$(DATASET_HEADER)' '\
		BEGIN { print header } \
		{ \
			line=$$0; \
			sub(/^.*\[DATASET_CSV\][[:space:]]*/, "", line); \
			if (line == "" || line == header) next; \
			print line; \
		}' > $(LOCAL_DATASET)

train-mopso:
	cd $(LB_MODULE_DIR) && go run ./cmd/mopso-train \
		-dataset $(LOCAL_DATASET) \
		-base $(LOCAL_BASE_PARAMS) \
		-out-params $(LOCAL_OPT_PARAMS) \
		-out-report $(LOCAL_TRAIN_REPORT) \
		-particles $(MOPSO_PARTICLES) \
		-iterations $(MOPSO_ITERATIONS) \
		-spread $(MOPSO_SPREAD) \
		-seed $(MOPSO_SEED) \
		-runs $(MOPSO_RUNS) \
		-allow-regression $(MOPSO_ALLOW_REGRESSION)

params-push:
	scp $(SCP_OPTS) $(LOCAL_OPT_PARAMS) $(REMOTE_ADDR):$(REMOTE_OPT_PARAMS)
	@echo "Optimized params pushed: $(REMOTE_OPT_PARAMS)"

offline-flow: dataset-pull train-mopso params-push

params-pull:
	@mkdir -p $(dir $(LOCAL_OPT_PARAMS))
	scp $(SCP_OPTS) $(REMOTE_ADDR):$(REMOTE_OPT_PARAMS) $(LOCAL_OPT_PARAMS)
	@echo "Optimized params pulled: $(LOCAL_OPT_PARAMS)"

dataset-reset-local:
	rm -f $(LOCAL_DATASET)
	@echo "Local dataset removed: $(LOCAL_DATASET)"

dataset-reset-remote:
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "rm -f $(REMOTE_DATASET)"
	@echo "Remote dataset removed: $(REMOTE_DATASET)"

reset-swarm:
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "\
		docker service update --detach=true --force $(LB_SERVICE) && \
		docker service update --detach=true --force $(NODE1_SERVICE) && \
		docker service update --detach=true --force $(NODE2_SERVICE) && \
		sleep 5"
	@echo "Swarm services have been force-updated and stabilized."

prep-fuzzy:
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "docker service update --detach=true --env-rm FUZZY_PARAM_SOURCE --env-rm TRAFFIC_LOG_MODE --env-add FUZZY_PARAM_SOURCE=base --env-add TRAFFIC_LOG_MODE=per_hit --label-add $(LB_MODE_LABEL_KEY)=fuzzy-base $(LB_SERVICE)"
	@$(MAKE) reset-swarm
	@$(MAKE) verify-fuzzy

prep-mopso:
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "docker service update --detach=true --env-rm FUZZY_PARAM_SOURCE --env-add FUZZY_PARAM_SOURCE=optimized --env-rm TRAFFIC_LOG_MODE --label-add $(LB_MODE_LABEL_KEY)=mopso-optimized $(LB_SERVICE)"
	@$(MAKE) reset-swarm
	@$(MAKE) verify-mopso

show-lb-source:
	@echo "=== LB Service Spec ($(LB_SERVICE)) ==="
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		echo "[ENV:SERVICE_SPEC]"; \
		docker service inspect $(LB_SERVICE) --format "{{range .Spec.TaskTemplate.ContainerSpec.Env}}{{println .}}{{end}}" | grep -E "^(FUZZY_PARAM_SOURCE|TRAFFIC_LOG_MODE)=" || true; \
		echo "[LABEL:SERVICE_SPEC]"; \
		docker service inspect $(LB_SERVICE) --format "{{json .Spec.Labels}}"'
	@echo "=== LB Running Task (Actual Container) ==="
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		cid=$$(docker ps --filter label=com.docker.swarm.service.name=$(LB_SERVICE) --format "{{.ID}}" | head -n1); \
		if [ -z "$$cid" ]; then echo "LB container belum running"; exit 1; fi; \
		echo CONTAINER_ID=$$cid; \
		echo "[ENV:CONTAINER_RUNTIME]"; \
		docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -E "^(FUZZY_PARAM_SOURCE|TRAFFIC_LOG_MODE)=" || true'

verify-fuzzy:
	@echo "Verifying FUZZY baseline mode..."
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		for i in $$(seq 1 20); do \
			cid=$$(docker ps --filter label=com.docker.swarm.service.name=$(LB_SERVICE) --format "{{.ID}}" | head -n1); \
			if [ -n "$$cid" ] && \
			   docker service inspect $(LB_SERVICE) --format "{{json .Spec.Labels}}" | grep -q "\"$(LB_MODE_LABEL_KEY)\":\"fuzzy-base\"" && \
			   docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^FUZZY_PARAM_SOURCE=base$$" && \
			   docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^TRAFFIC_LOG_MODE=per_hit$$"; then \
				echo "OK: source=fuzzy-base, logging=per_hit"; \
				exit 0; \
			fi; \
			sleep 2; \
		done; \
		echo "FAILED: mode fuzzy belum aktif penuh"; \
		exit 1'
	@$(MAKE) show-lb-source

verify-mopso:
	@echo "Verifying MOPSO optimized mode..."
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		for i in $$(seq 1 20); do \
			cid=$$(docker ps --filter label=com.docker.swarm.service.name=$(LB_SERVICE) --format "{{.ID}}" | head -n1); \
			if [ -n "$$cid" ] && \
			   docker service inspect $(LB_SERVICE) --format "{{json .Spec.Labels}}" | grep -q "\"$(LB_MODE_LABEL_KEY)\":\"mopso-optimized\"" && \
			   docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^FUZZY_PARAM_SOURCE=optimized$$" && \
			   ! docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^TRAFFIC_LOG_MODE="; then \
				echo "OK: source=mopso-optimized, logging=off"; \
				exit 0; \
			fi; \
			sleep 2; \
		done; \
		echo "FAILED: mode mopso belum aktif penuh"; \
		exit 1'
	@$(MAKE) show-lb-source

logs-lb-source:
	@echo "=== LB Source Markers From Service Logs ($(LB_SERVICE)) ==="
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		docker service logs --raw --timestamps --since 20m --tail 400 $(LB_SERVICE) 2>&1 | grep -E "ENTRYPOINT\\]\\[PARAM-SOURCE|ENTRYPOINT\\]\\[PARAM-SNAPSHOT|\\[FUZZY\\]" || true'
