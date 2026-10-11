# Build a static, CGO-free binary and ship it in a distroless image.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /byge ./cmd/byge

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /byge /byge
ENV SERVER_PORT=17808
EXPOSE 17808
VOLUME ["/data"]
ENTRYPOINT ["/byge"]
CMD ["serve"]
