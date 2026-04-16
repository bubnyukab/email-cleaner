# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.2
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
WORKDIR /src

# Download dependencies — bind mounts point to backend/ where go.mod/go.sum live.
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,source=backend/go.sum,target=go.sum \
    --mount=type=bind,source=backend/go.mod,target=go.mod \
    go mod download -x

ARG TARGETARCH

# Build the binary by binding the backend/ directory as the module root.
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,source=backend,target=. \
    CGO_ENABLED=0 GOARCH=$TARGETARCH go build -o /bin/server .

FROM alpine:latest AS final

RUN --mount=type=cache,target=/var/cache/apk \
    apk --update add \
        ca-certificates \
        tzdata \
        && \
        update-ca-certificates

ARG UID=10001
RUN adduser \
    --disabled-password \
    --gecos "" \
    --home "/nonexistent" \
    --shell "/sbin/nologin" \
    --no-create-home \
    --uid "${UID}" \
    appuser
USER appuser

# Server binary runs from /app; migrations are resolved relative to this dir.
WORKDIR /app
COPY --from=build /bin/server /app/server
COPY backend/migrations /app/migrations

EXPOSE 8080

ENTRYPOINT [ "/app/server" ]
