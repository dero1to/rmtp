.PHONY: build test test-race vet lint clean server client help

## ビルド
build:
	go build ./...

## サーバー起動
server:
	go run ./cmd/server

## サーバー起動（FLV録画あり）
server-rec:
	go run ./cmd/server -save ./recordings

## クライアント起動（FLVファイル指定）
client:
	go run ./cmd/client -file $(FILE)

## テスト
test:
	go test ./... -v -timeout 60s

## テスト（データ競合検出あり）
test-race:
	go test ./... -v -race -timeout 60s

## 静的解析
vet:
	go vet ./...

## vet + test
lint: vet test

## バイナリのクリーン
clean:
	rm -f rmtp-server rmtp-client
	rm -rf recordings/

## サーバー・クライアントのバイナリ生成
build-bin:
	go build -o rmtp-server ./cmd/server
	go build -o rmtp-client ./cmd/client

## ヘルプ
help:
	@echo "使い方:"
	@echo "  make build        全パッケージをビルド"
	@echo "  make server       サーバーを起動（ポート1935）"
	@echo "  make server-rec   サーバーを起動（FLV録画あり）"
	@echo "  make client FILE=video.flv  FLVファイルを配信"
	@echo "  make test         テストを実行"
	@echo "  make test-race    テストを実行（データ競合検出）"
	@echo "  make vet          go vet を実行"
	@echo "  make lint         vet + test"
	@echo "  make build-bin    バイナリを生成"
	@echo "  make clean        生成物を削除"
