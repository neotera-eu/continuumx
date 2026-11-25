# Copyright 2026 Neotera.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Device Twins Resources Publisher (UP2DATE4SDV CID 93).

FROM golang:1.23.12-alpine3.21 AS builder

ARG GO_LDFLAGS

COPY . /go/src/github.com/neotera-eu/continuumx

RUN CGO_ENABLED=0 GO111MODULE=off go build -v -o /usr/local/bin/dtpublisher -ldflags "$GO_LDFLAGS -w -s" \
    github.com/neotera-eu/continuumx/cloud/cmd/dtpublisher


FROM alpine:3.21

COPY --from=builder /usr/local/bin/dtpublisher /usr/local/bin/dtpublisher

# The publisher only reads the Kubernetes API and listens on unprivileged ports.
USER 65532:65532

EXPOSE 8443 8080

ENTRYPOINT ["dtpublisher"]
CMD ["serve"]
