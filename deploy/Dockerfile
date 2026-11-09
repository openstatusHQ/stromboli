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
	go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opensre ./cmd/opensre

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="opensre" \
	org.opencontainers.image.description="An open-source AI SRE agent you host yourself" \
	org.opencontainers.image.source="https://github.com/openstatushq/opensre" \
	org.opencontainers.image.licenses="MIT"
COPY --from=build /out/opensre /usr/local/bin/opensre
ENV OPENSRE_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/opensre"]
CMD ["serve"]
