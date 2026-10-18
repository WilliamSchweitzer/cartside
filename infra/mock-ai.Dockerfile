FROM golang:1.24.0-bookworm AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
COPY backend/cmd/mock-ai ./cmd/mock-ai
COPY backend/internal/mockai ./internal/mockai
COPY backend/internal/aiprotocol ./internal/aiprotocol
RUN CGO_ENABLED=0 go build -trimpath -o /mock-ai ./cmd/mock-ai

FROM scratch
COPY --from=build /mock-ai /mock-ai
USER 65532:65532
EXPOSE 8090
ENTRYPOINT ["/mock-ai"]
