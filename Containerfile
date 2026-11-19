FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.4-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/nightdesk ./cmd/nightdesk \
    && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/irashack/night-desk" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.title="Night Desk"
COPY --from=build /out/nightdesk /nightdesk
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/nightdesk"]
CMD ["serve", "--config", "/data/config.yaml"]
