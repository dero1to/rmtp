package server

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/user/rmtp/pkg/amf0"
	"github.com/user/rmtp/pkg/chunk"
	"github.com/user/rmtp/pkg/handshake"
	"github.com/user/rmtp/pkg/message"
)

// rtmpClient はテスト用の簡易RTMPクライアント。
type rtmpClient struct {
	conn   net.Conn
	reader *chunk.Reader
	writer *chunk.Writer
}

// newTestClient はテスト用クライアントを作成してサーバーに接続する。
func newTestClient(t *testing.T, addr string) *rtmpClient {
	t.Helper()

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("接続失敗: %v", err)
	}

	if err := handshake.ClientHandshake(conn); err != nil {
		conn.Close()
		t.Fatalf("ハンドシェイク失敗: %v", err)
	}

	return &rtmpClient{
		conn:   conn,
		reader: chunk.NewReader(conn),
		writer: chunk.NewWriter(conn),
	}
}

// connect はconnectコマンドを送信して_resultを待つ。
func (c *rtmpClient) connect(t *testing.T, app string) {
	t.Helper()

	obj := amf0.NewObject()
	obj.Set("app", app)

	hdr, body, err := message.NewCommandMessage(0, "connect", float64(1), obj)
	if err != nil {
		t.Fatalf("connectメッセージ作成失敗: %v", err)
	}
	if err := c.writer.WriteMessage(hdr, body); err != nil {
		t.Fatalf("connect送信失敗: %v", err)
	}

	// サーバーからの応答を読む（Window Ack, Bandwidth, Chunk Size, _result）
	for {
		respHdr, respBody, err := c.reader.ReadMessage()
		if err != nil {
			t.Fatalf("応答受信失敗: %v", err)
		}

		switch respHdr.MessageTypeID {
		case message.TypeSetChunkSize:
			if len(respBody) >= 4 {
				newSize := binary.BigEndian.Uint32(respBody) & 0x7FFFFFFF
				c.reader.SetChunkSize(newSize)
			}
		case message.TypeCommandMessageAMF0:
			values, err := amf0.Decode(respBody)
			if err != nil {
				t.Fatalf("AMF0デコード失敗: %v", err)
			}
			if cmdName, ok := values[0].(string); ok && cmdName == "_result" {
				return // connect成功
			}
		}
	}
}

// createStream はcreateStreamを送信してstreamIDを取得する。
func (c *rtmpClient) createStream(t *testing.T) uint32 {
	t.Helper()

	hdr, body, err := message.NewCommandMessage(0, "createStream", float64(2), nil)
	if err != nil {
		t.Fatalf("createStreamメッセージ作成失敗: %v", err)
	}
	if err := c.writer.WriteMessage(hdr, body); err != nil {
		t.Fatalf("createStream送信失敗: %v", err)
	}

	for {
		respHdr, respBody, err := c.reader.ReadMessage()
		if err != nil {
			t.Fatalf("応答受信失敗: %v", err)
		}
		if respHdr.MessageTypeID == message.TypeCommandMessageAMF0 {
			values, err := amf0.Decode(respBody)
			if err != nil {
				t.Fatalf("AMF0デコード失敗: %v", err)
			}
			if cmdName, ok := values[0].(string); ok && cmdName == "_result" {
				if len(values) >= 4 {
					if id, ok := values[3].(float64); ok {
						return uint32(id)
					}
				}
			}
		}
	}
}

// publish はpublishコマンドを送信してonStatusを待つ。
func (c *rtmpClient) publish(t *testing.T, streamID uint32, streamKey string) {
	t.Helper()

	hdr, body, err := message.NewCommandMessage(streamID, "publish", float64(0), nil, streamKey, "live")
	if err != nil {
		t.Fatalf("publishメッセージ作成失敗: %v", err)
	}
	if err := c.writer.WriteMessage(hdr, body); err != nil {
		t.Fatalf("publish送信失敗: %v", err)
	}

	for {
		respHdr, respBody, err := c.reader.ReadMessage()
		if err != nil {
			t.Fatalf("応答受信失敗: %v", err)
		}
		if respHdr.MessageTypeID == message.TypeCommandMessageAMF0 {
			values, _ := amf0.Decode(respBody)
			if len(values) >= 1 {
				if cmdName, ok := values[0].(string); ok && cmdName == "onStatus" {
					return
				}
			}
		}
	}
}

// play はplayコマンドを送信してonStatusを待つ。
func (c *rtmpClient) play(t *testing.T, streamID uint32, streamKey string) {
	t.Helper()

	hdr, body, err := message.NewCommandMessage(streamID, "play", float64(0), nil, streamKey)
	if err != nil {
		t.Fatalf("playメッセージ作成失敗: %v", err)
	}
	if err := c.writer.WriteMessage(hdr, body); err != nil {
		t.Fatalf("play送信失敗: %v", err)
	}

	for {
		respHdr, respBody, err := c.reader.ReadMessage()
		if err != nil {
			t.Fatalf("応答受信失敗: %v", err)
		}
		if respHdr.MessageTypeID == message.TypeCommandMessageAMF0 {
			values, _ := amf0.Decode(respBody)
			if len(values) >= 1 {
				if cmdName, ok := values[0].(string); ok && cmdName == "onStatus" {
					return
				}
			}
		}
	}
}

func (c *rtmpClient) close() {
	c.conn.Close()
}

// startServer はテスト用サーバーを起動し、アドレスを返す。
func startServer(t *testing.T) (string, func()) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("リスナー起動失敗: %v", err)
	}

	s := New(Config{Addr: listener.Addr().String()})

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // リスナーが閉じられた
			}
			go s.handleConnection(conn)
		}
	}()

	return listener.Addr().String(), func() { listener.Close() }
}

// TestServerConnect はサーバーへのconnect処理をテストする。
func TestServerConnect(t *testing.T) {
	addr, cleanup := startServer(t)
	defer cleanup()

	client := newTestClient(t, addr)
	defer client.close()

	client.connect(t, "live")
}

// TestServerCreateStream はcreateStreamでstreamID=1が返ることをテストする。
func TestServerCreateStream(t *testing.T) {
	addr, cleanup := startServer(t)
	defer cleanup()

	client := newTestClient(t, addr)
	defer client.close()

	client.connect(t, "live")
	streamID := client.createStream(t)

	if streamID != 1 {
		t.Errorf("streamID = %d, want 1", streamID)
	}
}

// TestServerPublish は配信(publish)の開始をテストする。
func TestServerPublish(t *testing.T) {
	addr, cleanup := startServer(t)
	defer cleanup()

	client := newTestClient(t, addr)
	defer client.close()

	client.connect(t, "live")
	streamID := client.createStream(t)
	client.publish(t, streamID, "teststream")
}

// TestServerPublishAndPlay は配信者と視聴者間のメディアデータ中継をテストする。
func TestServerPublishAndPlay(t *testing.T) {
	addr, cleanup := startServer(t)
	defer cleanup()

	// 配信者
	publisher := newTestClient(t, addr)
	defer publisher.close()

	publisher.connect(t, "live")
	pubStreamID := publisher.createStream(t)
	publisher.publish(t, pubStreamID, "teststream")

	// 視聴者
	subscriber := newTestClient(t, addr)
	defer subscriber.close()

	subscriber.connect(t, "live")
	subStreamID := subscriber.createStream(t)
	subscriber.play(t, subStreamID, "teststream")

	// 配信者から音声データを送信
	audioData := []byte{0xAF, 0x01, 0x12, 0x34, 0x56}
	audioHdr, audioBody := message.NewAudioMessage(pubStreamID, 100, audioData)
	if err := publisher.writer.WriteMessage(audioHdr, audioBody); err != nil {
		t.Fatalf("音声データ送信失敗: %v", err)
	}

	// 視聴者が音声データを受信するのを待つ
	subscriber.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		hdr, body, err := subscriber.reader.ReadMessage()
		if err != nil {
			t.Fatalf("視聴者のデータ受信失敗: %v", err)
		}
		if hdr.MessageTypeID == message.TypeAudioMessage {
			if len(body) != len(audioData) {
				t.Errorf("音声データ長 = %d, want %d", len(body), len(audioData))
			}
			for i := range body {
				if body[i] != audioData[i] {
					t.Errorf("音声データ[%d] = 0x%02x, want 0x%02x", i, body[i], audioData[i])
				}
			}
			return // 成功
		}
	}
}

// TestServerSequenceHeaderCache はSequence Headerのキャッシュをテストする。
// 配信者がSequence Headerを送信した後に視聴者が接続すると、
// キャッシュされたヘッダが自動的に送信されることを確認する。
func TestServerSequenceHeaderCache(t *testing.T) {
	addr, cleanup := startServer(t)
	defer cleanup()

	// 配信者
	publisher := newTestClient(t, addr)
	defer publisher.close()

	publisher.connect(t, "live")
	pubStreamID := publisher.createStream(t)
	publisher.publish(t, pubStreamID, "cachetest")

	// AVC Sequence Header を送信 (コーデック=AVC(0x07), パケットタイプ=0)
	// 先頭バイト: 0x17 = キーフレーム(1) + AVC(7)
	// 2バイト目: 0x00 = Sequence Header
	avcSeqHeader := []byte{0x17, 0x00, 0x00, 0x00, 0x00, 0x01, 0x64}
	videoHdr, videoBody := message.NewVideoMessage(pubStreamID, 0, avcSeqHeader)
	if err := publisher.writer.WriteMessage(videoHdr, videoBody); err != nil {
		t.Fatalf("映像ヘッダ送信失敗: %v", err)
	}

	// AAC Sequence Header を送信 (コーデック=AAC(0x0A=10), パケットタイプ=0)
	// 先頭バイト: 0xAF = AAC(10<<4|0x0F)
	// 2バイト目: 0x00 = Sequence Header
	aacSeqHeader := []byte{0xAF, 0x00, 0x12, 0x10}
	audioHdr, audioBody := message.NewAudioMessage(pubStreamID, 0, aacSeqHeader)
	if err := publisher.writer.WriteMessage(audioHdr, audioBody); err != nil {
		t.Fatalf("音声ヘッダ送信失敗: %v", err)
	}

	// 少し待って確実にサーバーが処理するのを待つ
	time.Sleep(100 * time.Millisecond)

	// 視聴者が後から接続
	subscriber := newTestClient(t, addr)
	defer subscriber.close()

	subscriber.connect(t, "live")
	subStreamID := subscriber.createStream(t)

	// playを送信（この後にキャッシュされたヘッダが送られてくるはず）
	playHdr, playBody, _ := message.NewCommandMessage(subStreamID, "play", float64(0), nil, "cachetest")
	subscriber.writer.WriteMessage(playHdr, playBody)

	// 受信したメッセージを確認
	gotVideo := false
	gotAudio := false
	subscriber.conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	for {
		hdr, body, err := subscriber.reader.ReadMessage()
		if err != nil {
			break
		}
		switch hdr.MessageTypeID {
		case message.TypeVideoMessage:
			if len(body) >= 2 && body[0] == 0x17 && body[1] == 0x00 {
				gotVideo = true
			}
		case message.TypeAudioMessage:
			if len(body) >= 2 && body[0] == 0xAF && body[1] == 0x00 {
				gotAudio = true
			}
		}
		if gotVideo && gotAudio {
			break
		}
	}

	if !gotVideo {
		t.Error("映像Sequence Headerがキャッシュから送信されなかった")
	}
	if !gotAudio {
		t.Error("音声Sequence Headerがキャッシュから送信されなかった")
	}
}

// TestServerMultiplePublishers は異なるストリームキーで複数の配信者が
// 同時に配信できることをテストする。
func TestServerMultiplePublishers(t *testing.T) {
	addr, cleanup := startServer(t)
	defer cleanup()

	// 配信者1
	pub1 := newTestClient(t, addr)
	defer pub1.close()
	pub1.connect(t, "live")
	pub1StreamID := pub1.createStream(t)
	pub1.publish(t, pub1StreamID, "stream1")

	// 配信者2
	pub2 := newTestClient(t, addr)
	defer pub2.close()
	pub2.connect(t, "live")
	pub2StreamID := pub2.createStream(t)
	pub2.publish(t, pub2StreamID, "stream2")

	// 両方からデータを送信
	audio1 := []byte{0xAF, 0x01, 0x11}
	audio2 := []byte{0xAF, 0x01, 0x22}

	audioHdr1, audioBody1 := message.NewAudioMessage(pub1StreamID, 0, audio1)
	pub1.writer.WriteMessage(audioHdr1, audioBody1)

	audioHdr2, audioBody2 := message.NewAudioMessage(pub2StreamID, 0, audio2)
	pub2.writer.WriteMessage(audioHdr2, audioBody2)
}

// TestServerSetChunkSize はクライアントからのSet Chunk Sizeが
// 正しく処理されることをテストする。
func TestServerSetChunkSize(t *testing.T) {
	addr, cleanup := startServer(t)
	defer cleanup()

	client := newTestClient(t, addr)
	defer client.close()

	client.connect(t, "live")

	// クライアントからSet Chunk Sizeを送信
	csHdr, csBody := message.NewSetChunkSize(8192)
	if err := client.writer.WriteMessage(csHdr, csBody); err != nil {
		t.Fatalf("Set Chunk Size送信失敗: %v", err)
	}
	client.writer.SetChunkSize(8192)

	// 変更後のチャンクサイズでcreateStreamが動作することを確認
	streamID := client.createStream(t)
	if streamID != 1 {
		t.Errorf("streamID = %d, want 1", streamID)
	}
}
