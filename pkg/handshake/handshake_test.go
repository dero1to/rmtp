package handshake

import (
	"net"
	"testing"
)

// TestHandshake はクライアント・サーバー間のハンドシェイクをテストする。
// 実際のTCP接続を使って双方向のハンドシェイクが成功することを確認する。
func TestHandshake(t *testing.T) {
	// テスト用のTCPリスナーを起動
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("リスナー起動失敗: %v", err)
	}
	defer listener.Close()

	errCh := make(chan error, 1)

	// サーバー側のgoroutine
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		errCh <- ServerHandshake(conn)
	}()

	// クライアント側
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("クライアント接続失敗: %v", err)
	}
	defer conn.Close()

	if err := ClientHandshake(conn); err != nil {
		t.Fatalf("クライアントハンドシェイク失敗: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("サーバーハンドシェイク失敗: %v", err)
	}
}
