# For ARM 32-bit
mkdir -p bin
GOARCH=arm GOOS=linux go build -o bin/systemctl-arm ./cmd/systemctl/
GOARCH=arm GOOS=linux go build -o bin/journalctl-arm ./cmd/journalctl/