# Builds the single static Codemail binary and ships it on a minimal base
# that has CA certificates and time zone data but no shell.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /codemail ./cmd/codemail

FROM gcr.io/distroless/static-debian12
COPY --from=build /codemail /codemail
VOLUME /data
EXPOSE 25 80 443 8080
ENTRYPOINT ["/codemail"]
CMD ["serve", "-data", "/data"]
