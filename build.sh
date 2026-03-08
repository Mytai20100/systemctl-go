print("Starting buildinnngg")
mkdir -p bin
go build -o bin/systemctl ./cmd/systemctl/
go build -o bin/journalctl ./cmd/journalctl/