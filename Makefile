.PHONY: build run-server run-cli test clean install deps

# Build the application
build:
	go build -o bin/albion-tracker .

# Run in server mode (Telegram bot)
run-server: build
	./bin/albion-tracker server

# Run CLI command examples
run-cli: build
	./bin/albion-tracker craft
	./bin/albion-tracker refine

# Test the application
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -rf bin/
	rm -f game_data.json

# Install dependencies
deps:
	go mod tidy
	go mod download

# Install the application
install: build
	sudo cp bin/albion-tracker /usr/local/bin/

# Initialize project (run after cloning)
init:
	go mod init albion-tracker || true
	go mod tidy
	cp .env.example .env
	echo "Edit .env file with your Telegram bot token"

# Docker commands
docker-build:
	docker build -t albion-tracker .

docker-run:
	docker run -d --name albion-tracker-bot --env-file .env albion-tracker

docker-stop:
	docker stop albion-tracker-bot
	docker rm albion-tracker-bot

# Development helpers
dev-server: build
	./bin/albion-tracker server

# Format code
fmt:
	go fmt ./...

# Run linter
lint:
	golangci-lint run

# Create sample data
sample-data:
	echo '{"users":{}}' > game_data.json