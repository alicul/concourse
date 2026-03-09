FROM concourse/concourse:local

ENV EDITOR=vim

RUN apk update && apk add --no-cache vim
RUN go install github.com/go-delve/delve/cmd/dlv@latest

ENTRYPOINT [ "dlv" ]
