FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /ac-evm ./cmd/ac-evm && \
    CGO_ENABLED=0 go build -trimpath -o /ac-evm-cli ./cmd/ac-evm-cli

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /ac-evm /ac-evm-cli /
COPY out /out
ENV ACEVM_DIR=/state
ENTRYPOINT ["/ac-evm", "-out", "/out"]
