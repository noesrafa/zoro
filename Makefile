GO ?= /usr/local/go/bin/go

.PHONY: build run vet tidy install logs restart status

build:
	$(GO) build -o bin/zoro ./cmd/zoro

run: build
	./bin/zoro

vet:
	$(GO) vet ./...

install:
	bash deploy/install.sh

logs:
	journalctl -u zoro -f

restart:
	sudo systemctl restart zoro

status:
	sudo systemctl --no-pager --full status zoro
