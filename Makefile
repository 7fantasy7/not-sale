# Makefile for NOT Back Contest Flash Sale Service

# Variables
SERVICE_NAME := not-sale-back
BUILD_DIR := build
MAIN_PATH := ./cmd/app

# Go commands
GO := go
GO_BUILD := $(GO) build
GO_TEST := $(GO) test
GO_CLEAN := $(GO) clean
GO_MOD := $(GO) mod
GO_GET := $(GO) get

# Docker commands
DOCKER := docker
DOCKER_COMPOSE := docker-compose

# Default target
.PHONY: all
all: build

# Download dependencies
.PHONY: deps
deps:
	@echo "Downloading dependencies..."
	$(GO_MOD) download

# Build the application
.PHONY: build
build: deps
	@echo "Building $(SERVICE_NAME)..."
	mkdir -p $(BUILD_DIR)
	$(GO_BUILD) -o $(BUILD_DIR)/$(SERVICE_NAME) $(MAIN_PATH)

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	$(GO_CLEAN)
	rm -rf $(BUILD_DIR)

# Run the application locally (without Docker)
.PHONY: run
run: build
	@echo "Running $(SERVICE_NAME)..."
	$(BUILD_DIR)/$(SERVICE_NAME)

# Start the Docker containers
.PHONY: docker-up
docker-up: build
	@echo "Starting Docker containers..."
	$(DOCKER_COMPOSE) up -d --build

# Stop the Docker containers
.PHONY: docker-down
docker-down:
	@echo "Stopping Docker containers..."
	$(DOCKER_COMPOSE) down

# View logs from Docker containers
.PHONY: docker-logs
docker-logs:
	@echo "Viewing Docker logs..."
	$(DOCKER_COMPOSE) logs -f
