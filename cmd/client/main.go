// =============================================================================
// RTMP クライアント（配信）起動プログラム
// =============================================================================
//
// 使い方:
//   go run ./cmd/client -file video.flv
//   go run ./cmd/client -addr localhost:1935 -app live -key streamkey -file video.flv
//
// FLVファイルをRTMPサーバーに配信する。
// FLVファイル内のタイムスタンプに基づいてリアルタイムペースで送信する。
//
// オプション:
//   -addr  サーバーアドレス（デフォルト: localhost:1935）
//   -app   アプリケーション名（デフォルト: live）
//   -key   ストリームキー（デフォルト: test）
//   -file  配信するFLVファイルのパス
//
package main

import (
	"flag"
	"log"
	"os"

	"github.com/user/rmtp/client"
)

func main() {
	// コマンドライン引数の解析
	addr := flag.String("addr", "localhost:1935", "サーバーアドレス")
	app := flag.String("app", "live", "アプリケーション名")
	key := flag.String("key", "test", "ストリームキー")
	filePath := flag.String("file", "", "配信するFLVファイル")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if *filePath == "" {
		log.Fatal("FLVファイルを指定してください: -file <path>")
	}

	// FLVファイルを開く
	f, err := os.Open(*filePath)
	if err != nil {
		log.Fatalf("ファイルを開けません: %v", err)
	}
	defer f.Close()

	// RTMPサーバーに接続し、配信を開始する
	log.Printf("サーバー %s に接続中...", *addr)
	log.Printf("配信設定: app=%s, key=%s", *app, *key)

	c, err := client.Publish(*addr, *app, *key)
	if err != nil {
		log.Fatalf("配信接続に失敗: %v", err)
	}
	defer c.Close()

	// FLVファイルの内容をRTMPで配信
	log.Printf("FLV配信開始: %s", *filePath)
	if err := c.PublishFLV(f); err != nil {
		log.Fatalf("配信エラー: %v", err)
	}

	log.Printf("配信完了")
}
