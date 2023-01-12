all: clean compile build

.PHONY: clean
clean:
	rm -f coredns-hosts-api

.PHONY: compile
compile:
	GOOS=linux CGO_ENABLED=0 go build -o coredns-hosts-api .

.PHONY: build
build:
	docker build -t coredns-hosts-api:$(VERSION) -f Dockerfile .

.PHONY: push
push:
