FROM alpine:3.8

# get this by:
# curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt
ARG CLIVERSION=v1.12.1

# need jq, some binaries and kubectl
RUN apk --update add curl gettext jq aws-cli

RUN curl -o /usr/local/bin/kubectl -LO https://storage.googleapis.com/kubernetes-release/release/${CLIVERSION}/bin/linux/amd64/kubectl
RUN chmod +x /usr/local/bin/kubectl

ADD entrypoint.sh /usr/local/bin/entrypoint

RUN addgroup asgroller && adduser  -D -G asgroller asgroller
USER asgroller
CMD ["/usr/local/bin/entrypoint"]
