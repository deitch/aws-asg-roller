FROM alpine:3.6 as certs

RUN apk add -U --no-cache ca-certificates


FROM scratch

COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY bin/aws-asg-roller-linux-amd64 /aws-asg-roller

CMD ["/aws-asg-roller"]
