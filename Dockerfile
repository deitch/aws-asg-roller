# base golang image
ARG GOVER="1.12.4-alpine3.9"
FROM golang:${GOVER} as golang

ARG REPO

RUN apk add -U --no-cache git ca-certificates

RUN GO111MODULE=off go get -u golang.org/x/lint/golint && GO111MODULE=off go get -u github.com/alecthomas/gometalinter

ENV GO111MODULE=on 
ENV CGO_ENABLED=0

WORKDIR /go/src/${REPO}

COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . .

# these have to be last steps so they do not bust the cache with each change
ARG OS
ARG ARCH
ENV GOOS=${OS} 
ENV GOARCH=${ARCH} 

# builder
FROM golang as build

RUN go build -v -i -o /usr/local/bin/aws-asg-roller

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/local/bin/aws-asg-roller /aws-asg-roller

CMD ["/aws-asg-roller"]
