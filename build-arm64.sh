# For ARM64
mkdir -p bin
GOARCH=arm64 GOOS=linux go build -o bin/systemctl-arm64 ./cmd/systemctl/
GOARCH=arm64 GOOS=linux go build -o bin/journalctl-arm64 ./cmd/journalctl/
