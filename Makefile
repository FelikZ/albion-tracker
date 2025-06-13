.PHONY: deploy registry-list build

deploy:
	docker buildx create --platform linux/arm/v7 --name "arm32-64" --driver "docker-container" --config buildkit-config.toml || true
	docker buildx build --builder arm32-64 --platform linux/arm/v7 -t 192.168.0.110:5000/albion-tracker:latest --push .
	docker buildx stop arm32-64

registry-list:
	@curl -sk http://192.168.0.110:5000/v2/_catalog | jq -r '.repositories[]' | while read -r repo; do tags=$$(curl -sk http://192.168.0.110:5000/v2/$$repo/tags/list | jq -r '.tags[]' 2>/dev/null); for tag in $$tags; do digest=$$(curl -sk -H "Accept: application/vnd.docker.distribution.manifest.v2+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.docker.distribution.manifest.v1+json" http://192.168.0.110:5000/v2/$$repo/manifests/$$tag | jq -r '.config.digest // .manifests[0].digest // .schemaVersion as $$sv | if $$sv == 1 then .layers[-1].digest else "not_found" end // "not_found"'); echo "{\"repository\":\"$$repo\",\"tag\":\"$$tag\",\"digest\":\"$$digest\"}"; done; done | jq -s .

build:
	go mod tidy
	go mod vendor
	go build -o bin/albion-tracker .
