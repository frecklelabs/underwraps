FROM mirror.gcr.io/library/golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/underwraps ./cmd/underwraps
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/underwraps /underwraps
USER 65532:65532
ENTRYPOINT ["/underwraps"]
