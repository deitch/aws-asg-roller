FROM golang:1.12 as builder

# install git
RUN apt-get update && apt-get install -y git curl ca-certificates
RUN curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh

WORKDIR /go/src/github.com/deitch/aws-asg-roller
COPY ./ ./

RUN dep ensure
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o aws-asg-roller .

FROM scratch

COPY --from=builder /go/src/github.com/deitch/aws-asg-roller/aws-asg-roller /aws-asg-roller
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
CMD ["/aws-asg-roller"]
