// =============================================================================
// RTMP サーバー起動プログラム
// =============================================================================
//
// 使い方:
//   go run ./cmd/server
//   go run ./cmd/server -addr :1935 -save ./recordings
//
// サーバーが起動したら、OBSやこのプロジェクトのクライアントから
// rtmp://localhost:1935/live/streamkey で配信できる。
//
// オプション:
//   -addr  リッスンアドレス（デフォルト: :1935）
//   -save  FLV保存ディレクトリ（指定しない場合は保存しない）
//
package main

import (
	"flag"
	"log"

	"github.com/user/rmtp/server"
)

func main() {
	// コマンドライン引数の解析
	addr := flag.String("addr", ":1935", "リッスンアドレス")
	saveDir := flag.String("save", "", "FLV保存ディレクトリ（省略時は保存しない）")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// サーバー設定
	config := server.Config{
		Addr:    *addr,
		SaveDir: *saveDir,
	}

	// サーバー起動
	s := server.New(config)
	log.Printf("RTMP サーバー起動中... アドレス: %s", *addr)
	if *saveDir != "" {
		log.Printf("FLV保存先: %s", *saveDir)
	}
	log.Printf("配信URL例: rtmp://localhost%s/live/streamkey", *addr)

	if err := s.ListenAndServe(); err != nil {
		log.Fatalf("サーバーエラー: %v", err)
	}
}
