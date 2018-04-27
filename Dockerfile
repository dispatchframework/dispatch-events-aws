FROM golang:1.10 as builder

WORKDIR ${GOPATH}/src/github.com/dispatchframework/dispatch-events-aws

COPY ["driver.go", "Gopkg.lock", "Gopkg.toml", "./"]

RUN go get -u github.com/golang/dep/cmd/dep
RUN dep ensure

RUN CGO_ENABLED=0 GOOS=linux go build -a -o dispatch-events-aws
RUN cp ./dispatch-events-aws /dispatch-events-aws


FROM scratch

ADD cacert-2018-03-07.pem /etc/ssl/certs/
COPY credentials /
COPY --from=builder /dispatch-events-aws /

ENTRYPOINT [ "/dispatch-events-aws" ]
