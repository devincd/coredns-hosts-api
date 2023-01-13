WHAT ?= coredns-hosts-server
HUB ?= docker.io/devincd

all: clean build
docker: docker-build docker-push

.PHONY: clean
clean:
	rm -f _output/$(WHAT)

.PHONY: build
build:
	CGO_ENABLED=0 go build -o _output/$(WHAT) cmd/$(WHAT)/main.go

.PHONY: docker-build
docker-build:
ifeq ($(VERSION), )
	echo "make docker-build command must set VERSION"
	exit 1
else
	DOCKER_BUILDKIT=0 docker build --no-cache -t $(HUB)/${WHAT}:$(VERSION) -f Dockerfile_${WHAT} .
endif

.PHONY: docker-push
docker-push:
ifeq ($(VERSION), )
	echo "make docker-push command must set VERSION"
	exit 1
endif
	docker push $(HUB)/${WHAT}:$(VERSION)
