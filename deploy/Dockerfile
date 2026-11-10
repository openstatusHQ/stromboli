# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
	go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/stromboli ./cmd/stromboli

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="stromboli" \
	org.opencontainers.image.description="An open-source AI SRE agent you host yourself" \
	org.opencontainers.image.source="https://github.com/openstatushq/stromboli" \
	org.opencontainers.image.licenses="MIT"
COPY --from=build /out/stromboli /usr/local/bin/stromboli
ENV STROMBOLI_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/stromboli"]
CMD ["serve"]
