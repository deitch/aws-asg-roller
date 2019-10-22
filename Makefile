.PHONY: all tag image push

IMAGE ?= deitch/aws-asg-roller
HASH ?= $(shell git show --format=%T -s)

# check if we should append a dirty tag
DIRTY ?= $(shell git status --short)
ifneq ($(DIRTY),)
TAG = $(HASH)-dirty
else
TAG = $(HASH)
endif

# BUILDARCH is the host architecture
# ARCH is the target architecture
# we need to keep track of them separately
BUILDARCH ?= $(shell uname -m)
BUILDOS ?= $(shell uname -s | tr A-Z a-z)

# canonicalized names for host architecture
ifeq ($(BUILDARCH),aarch64)
BUILDARCH=arm64
endif
ifeq ($(BUILDARCH),x86_64)
BUILDARCH=amd64
endif

# unless otherwise set, I am building for my own architecture and OS, i.e. not cross-compiling
ARCH ?= $(BUILDARCH)
OS ?= $(BUILDOS)
# canonicalized names for target architecture
ifeq ($(ARCH),aarch64)
override ARCH=arm64
endif
ifeq ($(ARCH),x86_64)
override ARCH=amd64
endif

PACKAGE_NAME ?= github.com/$(IMAGE)
IMGTAG = $(IMAGE):$(TAG)
BUILDERTAG = $(IMGTAG)-builder
BINDIR ?= bin
BINARY ?= $(BINDIR)/aws-asg-roller-$(OS)-$(ARCH)

GOVER ?= 1.12.4-alpine3.9

GO ?= GOOS=$(OS) GOARCH=$(ARCH) GO111MODULE=on CGO_ENABLED=0

ifneq ($(BUILD),local)
GO = docker run --rm $(BUILDERTAG)
endif

GOBIN ?= $(shell go env GOPATH)/bin
LINTER ?= $(GOBIN)/golangci-lint

GO_FILES := $(shell find . -type f -name '*.go')

.PHONY: all tag build image push test-start test-run test-run-interactive test-stop test build-test vendor
.PHONY: lint vet golint fmt-check ci cd

all: push

tag:
	@echo $(TAG)

gitstat:
	@git status

vendor:
ifeq ($(BUILD),local)
	$(GO) go mod download
endif

build: vendor $(BINARY)

$(BINDIR):
	mkdir -p $(BINDIR)

$(BINARY): $(BINDIR)
ifneq ($(BUILD),local)
	$(MAKE) image
	# because there is no way to `docker extract` or `docker cp` from an image
	CID=$$(docker create $(IMGTAG)) && \
	docker cp $${CID}:/aws-asg-roller $(BINARY) && \
	docker rm $${CID}
else
	$(GO) go build -v -i -o $(BINARY)
endif

image: gitstat
	docker build -t $(IMGTAG) --build-arg OS=$(OS) --build-arg ARCH=$(ARCH) --build-arg REPO=$(PACKAGE_NAME) --build-arg GOVER=$(GOVER) .

push: gitstat image
	docker push $(IMGTAG)

ci: gitstat tag build fmt-check lint test vet image

builder:
ifneq ($(BUILD),local)
	docker build -t $(BUILDERTAG) --build-arg OS=$(OS) --build-arg ARCH=$(ARCH) --build-arg REPO=$(PACKAGE_NAME) --build-arg GOVER=$(GOVER) --target=golang .
endif

fmt-check: builder
	if [ -n "$$($(GO) gofmt -l ${GO_FILES})" ]; then \
		$(GO) gofmt -s -e -d ${GO_FILES}; \
		exit 1; \
	fi

golangci-lint: $(LINTER)
$(LINTER):
ifeq ($(BUILD),local)
	$(GO) go get github.com/golangci/golangci-lint/cmd/golangci-lint@v1.17.1
endif

golint:
ifeq ($(BUILD),local)
ifeq (, $(shell which golint))
	$(GO) go get -u golang.org/x/lint/golint
endif
endif

## Lint files
lint: golint golangci-lint builder
	$(GO) $(LINTER) run -E golint -E gofmt ./...

## Run unit tests
test: builder
	# must run go test on my local arch and os
	$(GO) env GOOS= GOARCH= go test -short ./...

## Vet the files
vet: builder
	$(GO) go vet ./...

cd:
ifndef BRANCH_NAME
	$(error BRANCH_NAME is undefined - run using make <target> BRANCH_NAME=var or set an environment variable)
endif
	$(MAKE) push IMAGETAG=${BRANCH_NAME}
	$(MAKE) push IMAGETAG=${HASH}
