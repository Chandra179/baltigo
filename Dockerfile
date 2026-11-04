# Relay image: docker build -t baltigo . && docker run --env-file .env -p 4001:4001/tcp -p 4001:4001/udp -v baltigo-data:/data baltigo
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /baltigo .

FROM gcr.io/distroless/static-debian12
COPY --from=build /baltigo /baltigo
ENV RELAY_DB=/data/mailbox.db
VOLUME /data
ENTRYPOINT ["/baltigo", "--mode", "relay"]
