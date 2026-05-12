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

LOCAL_DATASET ?= $(LB_MODULE_DIR)/storage/fuzzy_training_data.csv
LOCAL_BASE_PARAMS ?= $(LB_MODULE_DIR)/configs/base_fuzzy_params.json
LOCAL_OPT_PARAMS ?= $(LB_MODULE_DIR)/storage/optimized_fuzzy_params.json
LOCAL_TRAIN_REPORT ?= $(LB_MODULE_DIR)/storage/mopso_offline_report.json

REMOTE_DATASET ?= $(LB_STORAGE_DIR)/fuzzy_training_data.csv
REMOTE_OPT_PARAMS ?= $(LB_CONFIGS_DIR)/optimized_fuzzy_params.json

LB_SERVICE ?= fmopso-stack_entry-point
NODE1_SERVICE ?= fmopso-stack_api-node1
NODE2_SERVICE ?= fmopso-stack_api-node2
LB_MODE_LABEL_KEY ?= fmopso.mode
LB_PUBLIC_SCHEME ?= http
LB_PUBLIC_PORT ?= 80

LB_IMAGE_REPO ?= pratamaze/lb
NODE_IMAGE_REPO ?= pratamaze/node
IMAGE_TAG ?= fmopso
FMOPSO_IMAGE_TAG ?= fmopso
FUZZY_IMAGE_TAG ?= fuzzy

MOPSO_PARTICLES ?= 50
MOPSO_ITERATIONS ?= 3000
MOPSO_SPREAD ?= 8
MOPSO_SEED ?= 0
MOPSO_RUNS ?= 5
MOPSO_ALLOW_REGRESSION ?= false
DATASET_LOG_SINCE ?= 20m
DATASET_CONTAINER_PATH ?= /storage/fuzzy_training_data.csv
LOCAL_CAPTURED_DATASET ?= ./logs/hasil_terbaru.csv
REMOTE_CAPTURED_DATASET ?= $(REMOTE_BASE_DIR)/logs/hasil_terbaru.csv

LOCUST_FILE ?= $(CURDIR)/tests/locust/locustfile.py
LOCUST_HOST ?= http://172.188.240.101
LOCUST_ENDPOINT_PATH ?= /api/stress-test?ms=50
LOCUST_OUT_DIR ?= $(CURDIR)/tests/locust/results

DATASET_HEADER := timestamp_utc,window_ms,traffic_log_mode,cpu_usage_unit,node1_name,node2_name,node1_requests,node2_requests,total_requests,node1_cpu_raw_usage,node2_cpu_raw_usage,node1_cpu_normalized_usage,node2_cpu_normalized_usage,node1_cpu_capacity,node2_cpu_capacity,node1_inflight,node2_inflight,node1_backend_inflight,node2_backend_inflight,node1_queue,node2_queue,node1_response_ms,node2_response_ms,node1_fuzzy_score,node2_fuzzy_score,os_idle_cpu

SSH_OPTS := -i $(SSH_KEY) -p $(SSH_PORT)
SCP_OPTS := -i $(SSH_KEY) -P $(SSH_PORT)
REMOTE_ADDR := $(REMOTE_USER)@$(REMOTE_HOST)

.PHONY: help image-build image-push image-build-push image-deploy image-update image-update-fmopso image-update-fuzzy dataset-pull dataset-capture-live capture-dataset train-mopso params-push offline-flow params-pull dataset-reset-local dataset-reset-remote reset-swarm prep-fuzzy prep-mopso prep-fmopso-realtime show-lb-source show-lb-runtime verify-fuzzy verify-mopso verify-fmopso-realtime logs-lb-source locust-normal locust-spike locust-ramp

help:
	@echo "Available targets:"
	@echo "  make image-update-fmopso # Build+push+redeploy image tag fmopso dan aktifkan mode fmopso"
	@echo "  make image-update-fuzzy  # Build+push+redeploy image tag fuzzy dan aktifkan mode fuzzy"
	@echo "  make image-update IMAGE_TAG=<tag> # Build+push+redeploy image custom tag"
	@echo "  make dataset-pull     # Snapshot dataset CSV dari stdout docker service logs"
	@echo "  make dataset-capture-live # Capture dataset live dari stdout docker service logs (Ctrl+C untuk stop)"
	@echo "  make capture-dataset  # Reset dataset di container, tunggu load test selesai, lalu ambil CSV terbaru"
	@echo "  make train-mopso      # Training MOPSO offline di laptop"
	@echo "  make params-push      # Push parameter optimized ke VPS (configs/optimized_fuzzy_params.json)"
	@echo "  make offline-flow     # Jalankan pull -> train -> push"
	@echo "  make params-pull      # Ambil parameter optimized dari VPS"
	@echo "  make dataset-reset-local"
	@echo "  make dataset-reset-remote"
	@echo "  make prep-fmopso-realtime # Aktifkan FMOPSO realtime (adaptive optimizer live)"
	@echo "  make show-lb-runtime   # Cek runtime status via HTTP /lb/runtime"
	@echo "  make logs-lb-source    # Tampilkan marker source parameter dari log entrypoint"
	@echo "  make locust-normal     # Jalankan load test Locust skenario normal (4 menit)"
	@echo "  make locust-spike      # Jalankan load test Locust skenario spike (5 menit)"
	@echo "  make locust-ramp       # Jalankan load test Locust skenario ramp/stress (5 menit)"

image-build:
	docker build -t $(LB_IMAGE_REPO):$(IMAGE_TAG) $(LB_MODULE_DIR)
	docker build -t $(NODE_IMAGE_REPO):$(IMAGE_TAG) $(CURDIR)/api-service
	@echo "Images built: $(LB_IMAGE_REPO):$(IMAGE_TAG), $(NODE_IMAGE_REPO):$(IMAGE_TAG)"

image-push:
	docker push $(LB_IMAGE_REPO):$(IMAGE_TAG)
	docker push $(NODE_IMAGE_REPO):$(IMAGE_TAG)
	@echo "Images pushed: $(LB_IMAGE_REPO):$(IMAGE_TAG), $(NODE_IMAGE_REPO):$(IMAGE_TAG)"

image-build-push: image-build image-push

image-deploy:
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "\
		docker service update --detach=true --with-registry-auth --force --image $(LB_IMAGE_REPO):$(IMAGE_TAG) $(LB_SERVICE) && \
		docker service update --detach=true --with-registry-auth --force --image $(NODE_IMAGE_REPO):$(IMAGE_TAG) $(NODE1_SERVICE) && \
		docker service update --detach=true --with-registry-auth --force --image $(NODE_IMAGE_REPO):$(IMAGE_TAG) $(NODE2_SERVICE)"
	@echo "Services updated to tag $(IMAGE_TAG)"

image-update: image-build-push image-deploy

image-update-fmopso: IMAGE_TAG=$(FMOPSO_IMAGE_TAG)
image-update-fmopso: image-update prep-fmopso-realtime

image-update-fuzzy: IMAGE_TAG=$(FUZZY_IMAGE_TAG)
image-update-fuzzy: image-update prep-fuzzy

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

capture-dataset:
	@mkdir -p ./logs
	@mkdir -p $(dir $(LOCAL_CAPTURED_DATASET))
	@cid=$$(ssh $(SSH_OPTS) $(REMOTE_ADDR) 'docker ps --filter label=com.docker.swarm.service.name=$(LB_SERVICE) --format "{{.ID}}" | head -n1'); \
	if [ -z "$$cid" ]; then \
		echo "LB container belum running untuk service $(LB_SERVICE)"; \
		exit 1; \
	fi; \
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "docker exec $$cid sh -c 'truncate -s 0 $(DATASET_CONTAINER_PATH) 2>/dev/null || : > $(DATASET_CONTAINER_PATH)'"; \
	echo "Dataset di-reset. Silakan jalankan JMeter/Locust sekarang. Tekan ENTER jika load test sudah selesai..."; \
	read -r _; \
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "mkdir -p $$(dirname $(REMOTE_CAPTURED_DATASET)) && docker cp $$cid:$(DATASET_CONTAINER_PATH) $(REMOTE_CAPTURED_DATASET)"; \
	tmp_file="$(LOCAL_CAPTURED_DATASET).tmp"; \
	scp $(SCP_OPTS) $(REMOTE_ADDR):$(REMOTE_CAPTURED_DATASET) "$$tmp_file"; \
	awk -v header='$(DATASET_HEADER)' '\
		BEGIN { print header } \
		{ \
			line=$$0; \
			gsub(/\r/, "", line); \
			if (line == "" || line == header) next; \
			print line; \
		}' "$$tmp_file" > $(LOCAL_CAPTURED_DATASET); \
	rm -f "$$tmp_file"; \
	echo "Dataset berhasil disimpan di ./logs/hasil_terbaru.csv"

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
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "docker service update --detach=true --env-rm LB_ALGO --env-add LB_ALGO=fuzzy --env-rm FUZZY_PARAM_SOURCE --env-rm TRAFFIC_LOG_MODE --env-rm MOPSO_BUSINESS_MODE --env-add MOPSO_BUSINESS_MODE=balanced --env-rm OPTIMIZER_INTERVAL --env-add OPTIMIZER_INTERVAL=1s --env-rm METRICS_INTERVAL --env-add METRICS_INTERVAL=100ms --env-rm ALGO_STATUS_LOG_INTERVAL --env-add ALGO_STATUS_LOG_INTERVAL=30s --env-add FUZZY_PARAM_SOURCE=base --env-add TRAFFIC_LOG_MODE=per_hit --label-add $(LB_MODE_LABEL_KEY)=fuzzy-base $(LB_SERVICE)"
	@$(MAKE) reset-swarm
	@$(MAKE) verify-fuzzy

prep-mopso:
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "docker service update --detach=true --env-rm LB_ALGO --env-add LB_ALGO=fuzzy --env-rm FUZZY_PARAM_SOURCE --env-add FUZZY_PARAM_SOURCE=optimized --env-rm TRAFFIC_LOG_MODE --env-add TRAFFIC_LOG_MODE=per_hit --env-rm MOPSO_BUSINESS_MODE --env-add MOPSO_BUSINESS_MODE=balanced --env-rm OPTIMIZER_INTERVAL --env-add OPTIMIZER_INTERVAL=1s --env-rm METRICS_INTERVAL --env-add METRICS_INTERVAL=250ms --env-rm ALGO_STATUS_LOG_INTERVAL --env-add ALGO_STATUS_LOG_INTERVAL=30s --label-add $(LB_MODE_LABEL_KEY)=mopso-optimized $(LB_SERVICE)"
	@$(MAKE) reset-swarm
	@$(MAKE) verify-mopso

prep-fmopso-realtime:
	ssh $(SSH_OPTS) $(REMOTE_ADDR) "docker service update --detach=true --env-rm LB_ALGO --env-add LB_ALGO=fmopso --env-rm FUZZY_PARAM_SOURCE --env-add FUZZY_PARAM_SOURCE=base --env-rm TRAFFIC_LOG_MODE --env-rm MOPSO_BUSINESS_MODE --env-add MOPSO_BUSINESS_MODE=balanced --env-rm OPTIMIZER_INTERVAL --env-add OPTIMIZER_INTERVAL=1s --env-rm METRICS_INTERVAL --env-add METRICS_INTERVAL=250ms --env-rm ALGO_STATUS_LOG_INTERVAL --env-add ALGO_STATUS_LOG_INTERVAL=30s --label-add $(LB_MODE_LABEL_KEY)=fmopso-realtime $(LB_SERVICE)"
	@$(MAKE) reset-swarm
	@$(MAKE) verify-fmopso-realtime

show-lb-source:
	@echo "=== LB Service Spec ($(LB_SERVICE)) ==="
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		echo "[ENV:SERVICE_SPEC]"; \
		docker service inspect $(LB_SERVICE) --format "{{range .Spec.TaskTemplate.ContainerSpec.Env}}{{println .}}{{end}}" | grep -E "^(LB_ALGO|FUZZY_PARAM_SOURCE|TRAFFIC_LOG_MODE|MOPSO_BUSINESS_MODE|METRICS_INTERVAL|OPTIMIZER_INTERVAL|ALGO_STATUS_LOG_INTERVAL)=" || true; \
		echo "[LABEL:SERVICE_SPEC]"; \
		docker service inspect $(LB_SERVICE) --format "{{json .Spec.Labels}}"'
	@echo "=== LB Running Task (Actual Container) ==="
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		cid=$$(docker ps --filter label=com.docker.swarm.service.name=$(LB_SERVICE) --format "{{.ID}}" | head -n1); \
		if [ -z "$$cid" ]; then echo "LB container belum running"; exit 1; fi; \
		echo CONTAINER_ID=$$cid; \
		echo "[ENV:CONTAINER_RUNTIME]"; \
		docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -E "^(LB_ALGO|FUZZY_PARAM_SOURCE|TRAFFIC_LOG_MODE|MOPSO_BUSINESS_MODE|METRICS_INTERVAL|OPTIMIZER_INTERVAL|ALGO_STATUS_LOG_INTERVAL)=" || true'

show-lb-runtime:
	@echo "=== LB Runtime API ($(LB_PUBLIC_SCHEME)://$(REMOTE_HOST):$(LB_PUBLIC_PORT)/lb/runtime) ==="
	curl -fsS "$(LB_PUBLIC_SCHEME)://$(REMOTE_HOST):$(LB_PUBLIC_PORT)/lb/runtime"

verify-fuzzy:
	@echo "Verifying FUZZY baseline mode..."
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		for i in $$(seq 1 20); do \
			cid=$$(docker ps --filter label=com.docker.swarm.service.name=$(LB_SERVICE) --format "{{.ID}}" | head -n1); \
			if [ -n "$$cid" ] && \
			   docker service inspect $(LB_SERVICE) --format "{{json .Spec.Labels}}" | grep -q "\"$(LB_MODE_LABEL_KEY)\":\"fuzzy-base\"" && \
			   docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^LB_ALGO=fuzzy$$" && \
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
			   docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^LB_ALGO=fuzzy$$" && \
			   docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^FUZZY_PARAM_SOURCE=optimized$$" && \
			   docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^TRAFFIC_LOG_MODE=per_hit$$"; then \
				echo "OK: source=mopso-optimized, logging=per_hit"; \
				exit 0; \
			fi; \
			sleep 2; \
		done; \
		echo "FAILED: mode mopso belum aktif penuh"; \
		exit 1'
	@$(MAKE) show-lb-source

verify-fmopso-realtime:
	@echo "Verifying FMOPSO realtime mode..."
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		for i in $$(seq 1 20); do \
			cid=$$(docker ps --filter label=com.docker.swarm.service.name=$(LB_SERVICE) --format "{{.ID}}" | head -n1); \
			if [ -n "$$cid" ] && \
			   docker service inspect $(LB_SERVICE) --format "{{json .Spec.Labels}}" | grep -q "\"$(LB_MODE_LABEL_KEY)\":\"fmopso-realtime\"" && \
			   docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^LB_ALGO=fmopso$$" && \
			   docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $$cid | grep -q "^MOPSO_BUSINESS_MODE=balanced$$"; then \
				echo "OK: algo=fmopso (realtime), mode=balanced"; \
				exit 0; \
			fi; \
			sleep 2; \
		done; \
		echo "FAILED: mode fmopso realtime belum aktif penuh"; \
		exit 1'
	@$(MAKE) show-lb-source

logs-lb-source:
	@echo "=== LB Source Markers From Service Logs ($(LB_SERVICE)) ==="
	ssh $(SSH_OPTS) $(REMOTE_ADDR) '\
		docker service logs --raw --timestamps --since 20m --tail 400 $(LB_SERVICE) 2>&1 | grep -E "ENTRYPOINT\\]\\[PARAM-SOURCE|ENTRYPOINT\\]\\[PARAM-SNAPSHOT|\\[FUZZY\\]|\\[RUNTIME\\]\\[ALGO-STATUS\\]|Memulai Load Balancer" || true'

locust-normal:
	@mkdir -p $(LOCUST_OUT_DIR)
	locust -f $(LOCUST_FILE) --host $(LOCUST_HOST) --headless --scenario normal --endpoint-path "$(LOCUST_ENDPOINT_PATH)" --csv $(LOCUST_OUT_DIR)/normal

locust-spike:
	@mkdir -p $(LOCUST_OUT_DIR)
	locust -f $(LOCUST_FILE) --host $(LOCUST_HOST) --headless --scenario spike --endpoint-path "$(LOCUST_ENDPOINT_PATH)" --csv $(LOCUST_OUT_DIR)/spike

locust-ramp:
	@mkdir -p $(LOCUST_OUT_DIR)
	locust -f $(LOCUST_FILE) --host $(LOCUST_HOST) --headless --scenario ramp --endpoint-path "$(LOCUST_ENDPOINT_PATH)" --csv $(LOCUST_OUT_DIR)/ramp
