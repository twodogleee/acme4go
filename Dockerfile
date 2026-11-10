# syntax=docker/dockerfile:1.7

FROM node:24-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.27-alpine AS backend
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=frontend /src/internal/web/dist/ ./internal/web/dist/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/acme4go ./cmd/acme4go
RUN mkdir -p /runtime/data /runtime/deployments && touch /runtime/data/.keep /runtime/deployments/.keep

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=backend --chown=65532:65532 /out/acme4go /app/acme4go
COPY --from=backend --chown=65532:65532 /runtime/ /app/
USER 65532:65532
EXPOSE 8080 9443
ENTRYPOINT ["/app/acme4go"]
CMD ["server"]
