# Multi-stage build: cgo-disabled static Go binary in a scratch image.
# https://docs.docker.com/build/building/multi-stage/
#
# Runtime configuration (env vars):
#   FLOOR_APPROVER_GROUP — authentik group whose members may mutate workspaces.
#   FLOOR_GIT_REPOSITORY — name of the GitRepository the sandbox repoints.
#   FLOOR_GITHUB_REPO    — owner/repo of the floor configuration repository.
#   FLOOR_NAMESPACE      — default namespace holding the Terraform objects.
#   FLOOR_ADDITIONAL_NAMESPACES — optional comma-separated workspace namespaces.
#   GITHUB_TOKEN         — token used to read and comment on pull requests.

FROM golang:1.27-alpine AS build
WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOFLAGS=-trimpath go build \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/floor-api \
      ./cmd/floor-api

FROM scratch
WORKDIR /
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/floor-api /floor-api
EXPOSE 8080
USER 1000:1000
ENTRYPOINT ["/floor-api"]
