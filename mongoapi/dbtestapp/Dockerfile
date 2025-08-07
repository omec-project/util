# SPDX-FileCopyrightText: 2022-present Intel Corporation
# Copyright 2019-present Open Networking Foundation
#
# SPDX-License-Identifier: Apache-2.0
#

FROM golang:1.24.6-bookworm AS test

LABEL maintainer="Aether SD-Core <dev@lists.aetherproject.org>"

RUN apt-get update && apt-get -y install vim

WORKDIR $GOPATH/src/dbtestapp
COPY . .
RUN go install

FROM alpine:3.22 AS dbtestapp
RUN apk update && apk add -U gcompat vim strace net-tools curl netcat-openbsd bind-tools bash

RUN mkdir -p /dbtestapp/bin
COPY --from=test /go/bin/* /dbtestapp/bin/
WORKDIR /dbtestapp
