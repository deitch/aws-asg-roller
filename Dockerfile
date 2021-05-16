# base golang image
ARG GOVER="1.16.5-alpine3.13"
FROM golang:${GOVER} as golang

ARG REPO

RUN apk add -U --no-cache git ca-certificates

RUN go get -u golang.org/x/lint/golint

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
