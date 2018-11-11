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

IMGTAG = $(IMAGE):$(TAG)

.PHONY: all tag build image push test-start test-run test-run-interactive test-stop test build-test

all: push

tag:
	@echo $(TAG)

build: image

image:
	docker build -t $(IMGTAG) .

push: image
	docker push $(IMGTAG)


