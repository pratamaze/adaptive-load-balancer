VPS_USER ?= ripe
VPS_IP ?= 172.188.240.101
REMOTE_PROJECT_DIR ?= /home/ripe/load-balancer
LB_SERVICE ?= fmopso-stack_entry-point
SSH_KEY ?= ~/.ssh/node-loadbalancer_key.pem

.PHONY: pull-logs train push-params verify-config cpu-snapshot ab-checklist

pull-logs:
	scp -i $(SSH_KEY) $(VPS_USER)@$(VPS_IP):$(REMOTE_PROJECT_DIR)/logs/traffic_dataset.csv ./load-balancer/logs/traffic_dataset.csv

train:
	cd load-balancer && go run ./cmd/offline_trainer/main.go

push-params:
	scp -i $(SSH_KEY) ./load-balancer/configs/optimized_fuzzy_params.json $(VPS_USER)@$(VPS_IP):$(REMOTE_PROJECT_DIR)/configs/optimized_fuzzy_params.json
# 	ssh $(VPS_USER)@$(VPS_IP) "docker service update --force $(LB_SERVICE)"

verify-config:
	ssh -i $(SSH_KEY) $(VPS_USER)@$(VPS_IP) "docker service inspect $(LB_SERVICE) --format '{{range .Spec.TaskTemplate.ContainerSpec.Env}}{{println .}}{{end}}' | egrep 'LB_ALGO|ACTIVE_ALGO|FUZZY_CONFIG_PATH|TRAFFIC_LOG_MODE|TRAFFIC_LOG_INTERVAL|OPTIMIZER_INTERVAL|METRICS_INTERVAL'"

cpu-snapshot:
	ssh -i $(SSH_KEY) $(VPS_USER)@$(VPS_IP) "docker ps --format '{{.Names}}' | egrep 'entry-point|api-node' | xargs -I{} docker stats --no-stream {}"

ab-checklist:
	@echo "A/B quick checklist:"
	@echo "1) Baseline phase: LB_ALGO=fuzzy ACTIVE_ALGO=FUZZY_MANUAL"
	@echo "2) Offline phase: run make train after pull-logs"
	@echo "3) Offline-tuned phase: LB_ALGO=fuzzy ACTIVE_ALGO=FUZZY_MOPSO_OFFLINE"
	@echo "4) Capture mode: TRAFFIC_LOG_MODE=per_hit (or window with TRAFFIC_LOG_INTERVAL=1s)"
	@echo "5) CPU guard: keep API node CPU peak <= 90% (avoid both nodes saturated at 100%)"
