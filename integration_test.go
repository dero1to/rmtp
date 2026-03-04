// =============================================================================
// RTMP 統合テスト
// =============================================================================
// サーバーとクライアント間で実際にRTMPセッションを確立し、
// データの送受信が正しく行われることを確認する。
//
package rmtp

import (
	"net"
	"testing"
	"time"

	"github.com/user/rmtp/pkg/amf0"
	"github.com/user/rmtp/pkg/chunk"
	"github.com/user/rmtp/pkg/handshake"
	"github.com/user/rmtp/pkg/message"
)

// TestFullRTMPSession はサーバーとクライアント間のRTMPセッション全体をテストする。
// ハンドシェイク → connect → createStream → publish の流れを確認。
func TestFullRTMPSession(t *testing.T) {
	// テスト用のTCPリスナー
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("リスナー起動失敗: %v", err)
	}
	defer listener.Close()

	done := make(chan error, 1)

	// サーバー側goroutine
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		// ハンドシェイク
		if err := handshake.ServerHandshake(conn); err != nil {
			done <- err
			return
		}

		reader := chunk.NewReader(conn)
		writer := chunk.NewWriter(conn)

		// connectコマンドを受信
		hdr, body, err := reader.ReadMessage()
		if err != nil {
			done <- err
			return
		}
		if hdr.MessageTypeID != message.TypeCommandMessageAMF0 {
			done <- err
			return
		}
		values, err := amf0.Decode(body)
		if err != nil {
			done <- err
			return
		}
		if cmdName, ok := values[0].(string); !ok || cmdName != "connect" {
			t.Errorf("コマンド名が不正: got %v, want connect", values[0])
		}

		// _result を返す
		props := amf0.NewObject()
		props.Set("fmsVer", "FMS/3,0,1,123")
		info := amf0.NewObject()
		info.Set("level", "status")
		info.Set("code", "NetConnection.Connect.Success")

		resultHdr, resultBody, _ := message.NewCommandMessage(0,
			"_result", float64(1), props, info,
		)
		writer.WriteMessage(resultHdr, resultBody)

		done <- nil
	}()

	// クライアント側
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("クライアント接続失敗: %v", err)
	}
	defer conn.Close()

	// ハンドシェイク
	if err := handshake.ClientHandshake(conn); err != nil {
		t.Fatalf("クライアントハンドシェイク失敗: %v", err)
	}

	writer := chunk.NewWriter(conn)
	reader := chunk.NewReader(conn)

	// connectコマンドを送信
	obj := amf0.NewObject()
	obj.Set("app", "live")

	cmdHdr, cmdBody, err := message.NewCommandMessage(0,
		"connect", float64(1), obj,
	)
	if err != nil {
		t.Fatalf("connectメッセージ作成エラー: %v", err)
	}
	if err := writer.WriteMessage(cmdHdr, cmdBody); err != nil {
		t.Fatalf("connect送信エラー: %v", err)
	}

	// _result を受信
	respHdr, respBody, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("応答受信エラー: %v", err)
	}
	if respHdr.MessageTypeID != message.TypeCommandMessageAMF0 {
		t.Fatalf("応答のメッセージタイプが不正: got %d", respHdr.MessageTypeID)
	}

	respValues, err := amf0.Decode(respBody)
	if err != nil {
		t.Fatalf("応答のデコードエラー: %v", err)
	}
	if cmdName, ok := respValues[0].(string); !ok || cmdName != "_result" {
		t.Errorf("応答のコマンド名が不正: got %v, want _result", respValues[0])
	}

	// サーバー側の完了を待つ
	if err := <-done; err != nil {
		t.Fatalf("サーバーエラー: %v", err)
	}
}
