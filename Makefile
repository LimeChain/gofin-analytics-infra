.PHONY: build test validate

build:
	docker build --platform linux/amd64 -t gofin-analytics-proxy:test proxy

test: build
	docker run --rm --platform linux/amd64 gofin-analytics-proxy:test caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
	python3 scripts/test_proxy.py

validate:
	terraform fmt -check -recursive
	terraform -chdir=bootstrap init -backend=false -input=false
	terraform -chdir=bootstrap validate
	terraform -chdir=infra init -backend=false -input=false
	terraform -chdir=infra validate
