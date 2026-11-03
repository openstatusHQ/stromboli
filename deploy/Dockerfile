FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opensre ./cmd/opensre

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/opensre /usr/local/bin/opensre
ENV OPENSRE_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/opensre"]
CMD ["serve"]
