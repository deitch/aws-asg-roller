.PHONY: all tag image push

IMAGE ?= deitch/aws-asg-roller
HASH ?= $(shell git show --format=%T -s)

# check if we should append a dirty tag
DIRTY ?= $(shell git diff-index --quiet HEAD -- ; echo $$?)
ifneq ($(DIRTY),0)
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

# unless otherwise set, I am building for my own architecture, i.e. not cross-compiling
ARCH ?= $(BUILDARCH)
# canonicalized names for target architecture
ifeq ($(ARCH),aarch64)
        override ARCH=arm64
endif
ifeq ($(ARCH),x86_64)
    override ARCH=amd64
endif
# unless otherwise set, I am building for my own OS, i.e. not cross-compiling
OS ?= $(BUILDOS)

PACKAGE_NAME ?= github.com/$(IMAGE)
IMGTAG = $(IMAGE):$(TAG)
BINDIR ?= bin
BINARY ?= $(BINDIR)/aws-asg-roller-$(OS)-$(ARCH)
BUILD_CMD ?= GOOS=$(OS) GOARCH=$(ARCH)

ifdef DOCKERBUILD
BUILDER ?= golang:1.11.2-alpine3.8
BUILD_CMD = docker run --rm \
    -e GOOS=$(OS) -e GOARCH=$(ARCH) \
		-e GOCACHE=/gocache \
		-v $(CURDIR)/.gocache:/gocache \
		-v $(CURDIR):/go/src/$(PACKAGE_NAME) \
 		-w /go/src/$(PACKAGE_NAME) \
		$(BUILDER)
endif

GO_FILES := $(shell find . -type f -not -path './vendor/*' -name '*.go')

pkgs:
ifndef PKG_LIST
	$(eval PKG_LIST := $(shell $(BUILD_CMD) go list ./... | grep -v vendor))
endif

.PHONY: all tag build image push test-start test-run test-run-interactive test-stop test build-test vendor
.PHONY: lint vet golint fmt-check dep ci cd

all: push

tag:
	echo $(TAG)

vendor: dep
	$(BUILD_CMD) dep ensure

## ensure we have dep installed
dep:
ifeq (, $(shell which dep))
	mkdir -p $$GOPATH/bin
	curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh
endif


build: vendor $(BINARY)

$(BINDIR):
	mkdir -p $(BINDIR)

$(BINARY): $(BINDIR)
	$(BUILD_CMD) go build -v -i -o $(BINARY)

image: $(BINARY)
	docker build -t $(IMGTAG) .

push: image
	docker push $(IMGTAG)

ci: build fmt-check lint test vet image

fmt-check:
	if [ -n "$(shell $(BUILD_CMD) gofmt -l ${GO_FILES})" ]; then \
		$(BUILD_CMD) gofmt -s -e -d ${GO_FILES}; \
		exit 1; \
	fi

gometalinter:
ifeq (, $(shell which gometalinter))
	go get -u github.com/alecthomas/gometalinter
endif

golint:
ifeq (, $(shell which golint))
	go get -u golang.org/x/lint/golint
endif

lint: pkgs golint gometalinter
	$(BUILD_CMD) gometalinter --disable-all --enable=golint  --vendor ./...

## Run unittests
test: pkgs
	$(BUILD_CMD) go test -short ${PKG_LIST}

## Vet the files
vet: pkgs
	$(BUILD_CMD) go vet ${PKG_LIST}

cd:
ifndef BRANCH_NAME
	$(error BRANCH_NAME is undefined - run using make <target> BRANCH_NAME=var or set an environment variable)
endif
	$(MAKE) push IMAGETAG=${BRANCH_NAME}
	$(MAKE) push IMAGETAG=${HASH}
