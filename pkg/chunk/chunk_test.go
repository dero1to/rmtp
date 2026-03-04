package chunk

import (
	"bytes"
	"testing"
)

// TestWriteReadMessage はチャンクの書き込みと読み取りをテストする。
// メッセージがチャンクに分割されて送信され、受信側で正しく再構築されることを確認。
func TestWriteReadMessage(t *testing.T) {
	buf := new(bytes.Buffer)

	// テスト用のメッセージ
	hdr := Header{
		ChunkStreamID:   3,
		Timestamp:       1000,
		MessageTypeID:   20, // コマンドメッセージ
		MessageStreamID: 1,
	}
	body := []byte("Hello, RTMP! This is a test message that should be chunked properly.")

	// 書き込み
	writer := NewWriter(buf)
	if err := writer.WriteMessage(hdr, body); err != nil {
		t.Fatalf("書き込みエラー: %v", err)
	}

	// 読み取り
	reader := NewReader(buf)
	readHdr, readBody, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("読み取りエラー: %v", err)
	}

	// ヘッダの検証
	if readHdr.ChunkStreamID != hdr.ChunkStreamID {
		t.Errorf("ChunkStreamID: got %d, want %d", readHdr.ChunkStreamID, hdr.ChunkStreamID)
	}
	if readHdr.Timestamp != hdr.Timestamp {
		t.Errorf("Timestamp: got %d, want %d", readHdr.Timestamp, hdr.Timestamp)
	}
	if readHdr.MessageTypeID != hdr.MessageTypeID {
		t.Errorf("MessageTypeID: got %d, want %d", readHdr.MessageTypeID, hdr.MessageTypeID)
	}
	if readHdr.MessageStreamID != hdr.MessageStreamID {
		t.Errorf("MessageStreamID: got %d, want %d", readHdr.MessageStreamID, hdr.MessageStreamID)
	}

	// ボディの検証
	if !bytes.Equal(readBody, body) {
		t.Errorf("Body: got %q, want %q", readBody, body)
	}
}

// TestLargeMessage は大きなメッセージのチャンク分割をテストする。
// デフォルトチャンクサイズ(128バイト)を超えるメッセージが
// 正しく分割・再構築されることを確認。
func TestLargeMessage(t *testing.T) {
	buf := new(bytes.Buffer)

	// 500バイトのメッセージ（128バイトのチャンクに4分割される）
	hdr := Header{
		ChunkStreamID:   3,
		Timestamp:       0,
		MessageTypeID:   9, // 映像メッセージ
		MessageStreamID: 1,
	}
	body := make([]byte, 500)
	for i := range body {
		body[i] = byte(i % 256)
	}

	writer := NewWriter(buf)
	if err := writer.WriteMessage(hdr, body); err != nil {
		t.Fatalf("書き込みエラー: %v", err)
	}

	reader := NewReader(buf)
	_, readBody, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("読み取りエラー: %v", err)
	}

	if !bytes.Equal(readBody, body) {
		t.Errorf("ボディが一致しない: got len=%d, want len=%d", len(readBody), len(body))
	}
}

// TestCustomChunkSize はカスタムチャンクサイズでの動作をテストする。
func TestCustomChunkSize(t *testing.T) {
	buf := new(bytes.Buffer)

	hdr := Header{
		ChunkStreamID:   3,
		Timestamp:       0,
		MessageTypeID:   20,
		MessageStreamID: 0,
	}
	body := make([]byte, 1000)
	for i := range body {
		body[i] = byte(i % 256)
	}

	// チャンクサイズを4096に変更
	writer := NewWriter(buf)
	writer.SetChunkSize(4096)
	if err := writer.WriteMessage(hdr, body); err != nil {
		t.Fatalf("書き込みエラー: %v", err)
	}

	reader := NewReader(buf)
	reader.SetChunkSize(4096)
	_, readBody, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("読み取りエラー: %v", err)
	}

	if !bytes.Equal(readBody, body) {
		t.Errorf("ボディが一致しない")
	}
}
