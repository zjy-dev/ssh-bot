# syntax=docker/dockerfile:1.7

# --- build stage ---
FROM golang:1.26.2-alpine AS build

WORKDIR /src

# Cache deps
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

# --- runtime stage ---
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/bot /bot
COPY configs/config.example.yaml /configs/config.example.yaml

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/bot"]
CMD ["--config", "/configs/config.yaml"]
