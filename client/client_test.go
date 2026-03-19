package client

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/user/rmtp/pkg/amf0"
	"github.com/user/rmtp/pkg/chunk"
	"github.com/user/rmtp/pkg/flv"
	"github.com/user/rmtp/pkg/handshake"
	"github.com/user/rmtp/pkg/message"
)

// mockServer はテスト用の簡易RTMPサーバーを起動する。
// connectから publish の応答までを処理する。
func mockServer(t *testing.T, listener net.Listener, done chan<- error) {
	t.Helper()

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
	cmdName, _ := values[0].(string)
	if cmdName != "connect" {
		t.Errorf("コマンド名 = %s, want connect", cmdName)
	}

	// connectのプロパティからapp名を確認
	if len(values) >= 3 {
		if obj, ok := values[2].(*amf0.Object); ok {
			if app, exists := obj.Get("app"); exists {
				if appStr, ok := app.(string); ok && appStr != "live" {
					t.Errorf("app = %s, want live", appStr)
				}
			}
		}
	}

	// Window Ack Size を送信
	ackHdr, ackBody := message.NewWindowAckSize(2500000)
	writer.WriteMessage(ackHdr, ackBody)

	// Set Peer Bandwidth を送信
	bwHdr, bwBody := message.NewSetPeerBandwidth(2500000, message.LimitTypeDynamic)
	writer.WriteMessage(bwHdr, bwBody)

	// Set Chunk Size を送信
	csHdr, csBody := message.NewSetChunkSize(4096)
	writer.WriteMessage(csHdr, csBody)
	writer.SetChunkSize(4096)

	// _result（connect成功）を送信
	props := amf0.NewObject()
	props.Set("fmsVer", "FMS/3,0,1,123")
	props.Set("capabilities", float64(31))
	info := amf0.NewObject()
	info.Set("level", "status")
	info.Set("code", "NetConnection.Connect.Success")

	resultHdr, resultBody, _ := message.NewCommandMessage(0, "_result", float64(1), props, info)
	writer.WriteMessage(resultHdr, resultBody)

	// createStreamを受信
	hdr, body, err = reader.ReadMessage()
	if err != nil {
		done <- err
		return
	}
	values, _ = amf0.Decode(body)
	cmdName, _ = values[0].(string)
	if cmdName != "createStream" {
		t.Errorf("コマンド名 = %s, want createStream", cmdName)
	}

	txnID := float64(2)
	if len(values) >= 2 {
		if id, ok := values[1].(float64); ok {
			txnID = id
		}
	}

	// _result（streamID=1）を送信
	csResultHdr, csResultBody, _ := message.NewCommandMessage(0, "_result", txnID, nil, float64(1))
	writer.WriteMessage(csResultHdr, csResultBody)

	// publishを受信
	hdr, body, err = reader.ReadMessage()
	if err != nil {
		done <- err
		return
	}
	values, _ = amf0.Decode(body)
	cmdName, _ = values[0].(string)
	if cmdName != "publish" {
		t.Errorf("コマンド名 = %s, want publish", cmdName)
	}

	// onStatus を送信
	status := amf0.NewObject()
	status.Set("level", "status")
	status.Set("code", "NetStream.Publish.Start")
	statusHdr, statusBody, _ := message.NewCommandMessage(1, "onStatus", float64(0), nil, status)
	writer.WriteMessage(statusHdr, statusBody)

	done <- nil
}

// TestPublish はクライアントのPublish関数をテストする。
func TestPublish(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("リスナー起動失敗: %v", err)
	}
	defer listener.Close()

	done := make(chan error, 1)
	go mockServer(t, listener, done)

	client, err := Publish(listener.Addr().String(), "live", "testkey")
	if err != nil {
		t.Fatalf("Publish失敗: %v", err)
	}
	defer client.Close()

	if client.app != "live" {
		t.Errorf("app = %s, want live", client.app)
	}
	if client.streamKey != "testkey" {
		t.Errorf("streamKey = %s, want testkey", client.streamKey)
	}
	if client.streamID != 1 {
		t.Errorf("streamID = %d, want 1", client.streamID)
	}

	if err := <-done; err != nil {
		t.Fatalf("サーバーエラー: %v", err)
	}
}

// TestSendAudio は音声データ送信をテストする。
func TestSendAudio(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("リスナー起動失敗: %v", err)
	}
	defer listener.Close()

	audioDone := make(chan []byte, 1)
	done := make(chan error, 1)

	go func() {
		// mockServerの処理 + 音声データ受信
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		handshake.ServerHandshake(conn)
		reader := chunk.NewReader(conn)
		writer := chunk.NewWriter(conn)

		// connect処理
		reader.ReadMessage()
		ackHdr, ackBody := message.NewWindowAckSize(2500000)
		writer.WriteMessage(ackHdr, ackBody)
		bwHdr, bwBody := message.NewSetPeerBandwidth(2500000, message.LimitTypeDynamic)
		writer.WriteMessage(bwHdr, bwBody)
		csHdr, csBody := message.NewSetChunkSize(4096)
		writer.WriteMessage(csHdr, csBody)
		writer.SetChunkSize(4096)

		props := amf0.NewObject()
		props.Set("fmsVer", "FMS/3,0,1,123")
		info := amf0.NewObject()
		info.Set("level", "status")
		info.Set("code", "NetConnection.Connect.Success")
		rHdr, rBody, _ := message.NewCommandMessage(0, "_result", float64(1), props, info)
		writer.WriteMessage(rHdr, rBody)

		// createStream処理
		reader.ReadMessage()
		csrHdr, csrBody, _ := message.NewCommandMessage(0, "_result", float64(2), nil, float64(1))
		writer.WriteMessage(csrHdr, csrBody)

		// publish処理
		reader.ReadMessage()
		status := amf0.NewObject()
		status.Set("level", "status")
		status.Set("code", "NetStream.Publish.Start")
		sHdr, sBody, _ := message.NewCommandMessage(1, "onStatus", float64(0), nil, status)
		writer.WriteMessage(sHdr, sBody)

		// 音声データを受信
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			hdr, body, err := reader.ReadMessage()
			if err != nil {
				done <- nil
				return
			}
			if hdr.MessageTypeID == message.TypeAudioMessage {
				audioDone <- body
				done <- nil
				return
			}
		}
	}()

	client, err := Publish(listener.Addr().String(), "live", "testkey")
	if err != nil {
		t.Fatalf("Publish失敗: %v", err)
	}
	defer client.Close()

	// 音声データを送信
	audioData := []byte{0xAF, 0x01, 0x12, 0x34}
	if err := client.SendAudio(100, audioData); err != nil {
		t.Fatalf("SendAudio失敗: %v", err)
	}

	select {
	case received := <-audioDone:
		if !bytes.Equal(received, audioData) {
			t.Errorf("受信データ = %v, want %v", received, audioData)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("音声データの受信タイムアウト")
	}

	<-done
}

// TestSendVideo は映像データ送信をテストする。
func TestSendVideo(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("リスナー起動失敗: %v", err)
	}
	defer listener.Close()

	videoDone := make(chan []byte, 1)
	done := make(chan error, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		handshake.ServerHandshake(conn)
		reader := chunk.NewReader(conn)
		writer := chunk.NewWriter(conn)

		// connect
		reader.ReadMessage()
		ackHdr, ackBody := message.NewWindowAckSize(2500000)
		writer.WriteMessage(ackHdr, ackBody)
		bwHdr, bwBody := message.NewSetPeerBandwidth(2500000, message.LimitTypeDynamic)
		writer.WriteMessage(bwHdr, bwBody)
		csHdr, csBody := message.NewSetChunkSize(4096)
		writer.WriteMessage(csHdr, csBody)
		writer.SetChunkSize(4096)
		props := amf0.NewObject()
		props.Set("fmsVer", "FMS/3,0,1,123")
		info := amf0.NewObject()
		info.Set("level", "status")
		info.Set("code", "NetConnection.Connect.Success")
		rHdr, rBody, _ := message.NewCommandMessage(0, "_result", float64(1), props, info)
		writer.WriteMessage(rHdr, rBody)

		// createStream
		reader.ReadMessage()
		csrHdr, csrBody, _ := message.NewCommandMessage(0, "_result", float64(2), nil, float64(1))
		writer.WriteMessage(csrHdr, csrBody)

		// publish
		reader.ReadMessage()
		status := amf0.NewObject()
		status.Set("level", "status")
		status.Set("code", "NetStream.Publish.Start")
		sHdr, sBody, _ := message.NewCommandMessage(1, "onStatus", float64(0), nil, status)
		writer.WriteMessage(sHdr, sBody)

		// 映像データを受信
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			hdr, body, err := reader.ReadMessage()
			if err != nil {
				done <- nil
				return
			}
			if hdr.MessageTypeID == message.TypeVideoMessage {
				videoDone <- body
				done <- nil
				return
			}
		}
	}()

	client, err := Publish(listener.Addr().String(), "live", "testkey")
	if err != nil {
		t.Fatalf("Publish失敗: %v", err)
	}
	defer client.Close()

	videoData := []byte{0x17, 0x01, 0x00, 0x00, 0x00, 0xAB, 0xCD}
	if err := client.SendVideo(200, videoData); err != nil {
		t.Fatalf("SendVideo失敗: %v", err)
	}

	select {
	case received := <-videoDone:
		if !bytes.Equal(received, videoData) {
			t.Errorf("受信データ = %v, want %v", received, videoData)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("映像データの受信タイムアウト")
	}

	<-done
}

// TestSendMetadata はメタデータ送信をテストする。
func TestSendMetadata(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("リスナー起動失敗: %v", err)
	}
	defer listener.Close()

	metaDone := make(chan []byte, 1)
	done := make(chan error, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		handshake.ServerHandshake(conn)
		reader := chunk.NewReader(conn)
		writer := chunk.NewWriter(conn)

		// connect
		reader.ReadMessage()
		ackHdr, ackBody := message.NewWindowAckSize(2500000)
		writer.WriteMessage(ackHdr, ackBody)
		bwHdr, bwBody := message.NewSetPeerBandwidth(2500000, message.LimitTypeDynamic)
		writer.WriteMessage(bwHdr, bwBody)
		csHdr, csBody := message.NewSetChunkSize(4096)
		writer.WriteMessage(csHdr, csBody)
		writer.SetChunkSize(4096)
		props := amf0.NewObject()
		props.Set("fmsVer", "FMS/3,0,1,123")
		info := amf0.NewObject()
		info.Set("level", "status")
		info.Set("code", "NetConnection.Connect.Success")
		rHdr, rBody, _ := message.NewCommandMessage(0, "_result", float64(1), props, info)
		writer.WriteMessage(rHdr, rBody)

		// createStream
		reader.ReadMessage()
		csrHdr, csrBody, _ := message.NewCommandMessage(0, "_result", float64(2), nil, float64(1))
		writer.WriteMessage(csrHdr, csrBody)

		// publish
		reader.ReadMessage()
		status := amf0.NewObject()
		status.Set("level", "status")
		status.Set("code", "NetStream.Publish.Start")
		sHdr, sBody, _ := message.NewCommandMessage(1, "onStatus", float64(0), nil, status)
		writer.WriteMessage(sHdr, sBody)

		// メタデータを受信
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			hdr, body, err := reader.ReadMessage()
			if err != nil {
				done <- nil
				return
			}
			if hdr.MessageTypeID == message.TypeDataMessageAMF0 {
				metaDone <- body
				done <- nil
				return
			}
		}
	}()

	client, err := Publish(listener.Addr().String(), "live", "testkey")
	if err != nil {
		t.Fatalf("Publish失敗: %v", err)
	}
	defer client.Close()

	metadata := amf0.NewObject()
	metadata.Set("width", float64(1920))
	metadata.Set("height", float64(1080))

	if err := client.SendMetadata(metadata); err != nil {
		t.Fatalf("SendMetadata失敗: %v", err)
	}

	select {
	case received := <-metaDone:
		values, err := amf0.Decode(received)
		if err != nil {
			t.Fatalf("メタデータデコード失敗: %v", err)
		}
		if len(values) < 1 {
			t.Fatal("メタデータの値が空")
		}
		if name, ok := values[0].(string); !ok || name != "@setDataFrame" {
			t.Errorf("メタデータ名 = %v, want @setDataFrame", values[0])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("メタデータの受信タイムアウト")
	}

	<-done
}

// TestPublishFLV はFLVファイルの配信をテストする。
func TestPublishFLV(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("リスナー起動失敗: %v", err)
	}
	defer listener.Close()

	receivedTags := make(chan struct {
		typeID byte
		data   []byte
	}, 10)
	done := make(chan error, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		handshake.ServerHandshake(conn)
		reader := chunk.NewReader(conn)
		writer := chunk.NewWriter(conn)

		// connect
		reader.ReadMessage()
		ackHdr, ackBody := message.NewWindowAckSize(2500000)
		writer.WriteMessage(ackHdr, ackBody)
		bwHdr, bwBody := message.NewSetPeerBandwidth(2500000, message.LimitTypeDynamic)
		writer.WriteMessage(bwHdr, bwBody)
		csHdr, csBody := message.NewSetChunkSize(4096)
		writer.WriteMessage(csHdr, csBody)
		writer.SetChunkSize(4096)
		props := amf0.NewObject()
		props.Set("fmsVer", "FMS/3,0,1,123")
		info := amf0.NewObject()
		info.Set("level", "status")
		info.Set("code", "NetConnection.Connect.Success")
		rHdr, rBody, _ := message.NewCommandMessage(0, "_result", float64(1), props, info)
		writer.WriteMessage(rHdr, rBody)

		// createStream
		reader.ReadMessage()
		csrHdr, csrBody, _ := message.NewCommandMessage(0, "_result", float64(2), nil, float64(1))
		writer.WriteMessage(csrHdr, csrBody)

		// publish
		reader.ReadMessage()
		status := amf0.NewObject()
		status.Set("level", "status")
		status.Set("code", "NetStream.Publish.Start")
		sHdr, sBody, _ := message.NewCommandMessage(1, "onStatus", float64(0), nil, status)
		writer.WriteMessage(sHdr, sBody)

		// FLVから送られてくるメディアデータを受信
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			hdr, body, err := reader.ReadMessage()
			if err != nil {
				done <- nil
				return
			}
			if hdr.MessageTypeID == message.TypeAudioMessage ||
				hdr.MessageTypeID == message.TypeVideoMessage {
				receivedTags <- struct {
					typeID byte
					data   []byte
				}{hdr.MessageTypeID, body}
			}
		}
	}()

	client, err := Publish(listener.Addr().String(), "live", "testkey")
	if err != nil {
		t.Fatalf("Publish失敗: %v", err)
	}
	defer client.Close()

	// テスト用FLVデータをメモリ上で作成
	var flvBuf bytes.Buffer
	flvWriter, err := flv.NewWriter(&flvBuf, true, true)
	if err != nil {
		t.Fatalf("FLVWriter作成失敗: %v", err)
	}

	// 音声タグと映像タグを書き込む
	audioData := []byte{0xAF, 0x01, 0x11, 0x22}
	videoData := []byte{0x17, 0x01, 0x33, 0x44}
	flvWriter.WriteTag(flv.TagTypeAudio, 0, audioData)
	flvWriter.WriteTag(flv.TagTypeVideo, 0, videoData)

	// FLVデータを配信
	flvReader := bytes.NewReader(flvBuf.Bytes())
	if err := client.PublishFLV(flvReader); err != nil {
		t.Fatalf("PublishFLV失敗: %v", err)
	}

	// 受信したデータを検証
	gotAudio := false
	gotVideo := false
	timeout := time.After(3 * time.Second)

	for !gotAudio || !gotVideo {
		select {
		case tag := <-receivedTags:
			switch tag.typeID {
			case message.TypeAudioMessage:
				if bytes.Equal(tag.data, audioData) {
					gotAudio = true
				}
			case message.TypeVideoMessage:
				if bytes.Equal(tag.data, videoData) {
					gotVideo = true
				}
			}
		case <-timeout:
			if !gotAudio {
				t.Error("音声データを受信できなかった")
			}
			if !gotVideo {
				t.Error("映像データを受信できなかった")
			}
			return
		}
	}
}

// TestPublishConnectionTimeout は接続先が存在しない場合にタイムアウトすることをテストする。
func TestPublishConnectionTimeout(t *testing.T) {
	// 接続を受け付けないアドレス
	_, err := Publish("192.0.2.1:1935", "live", "test")
	if err == nil {
		t.Fatal("エラーが返されなかった")
	}
}

// TestClose はClose関数が正しく動作することをテストする。
func TestClose(t *testing.T) {
	// nilのconnでもpanicしないことを確認
	c := &Client{}
	if err := c.Close(); err != nil {
		t.Errorf("Close error = %v, want nil", err)
	}
}

// TestHandleProtocolMessage はプロトコルメッセージの処理をテストする。
func TestHandleProtocolMessage(t *testing.T) {
	c := &Client{
		reader: chunk.NewReader(bytes.NewReader(nil)),
	}

	// Set Chunk Size
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, 8192)
	c.handleProtocolMessage(chunk.Header{MessageTypeID: message.TypeSetChunkSize}, body)

	// Window Ack Size（panicしないことを確認）
	c.handleProtocolMessage(chunk.Header{MessageTypeID: message.TypeWindowAckSize}, body)

	// Set Peer Bandwidth（panicしないことを確認）
	bwBody := make([]byte, 5)
	binary.BigEndian.PutUint32(bwBody, 2500000)
	bwBody[4] = message.LimitTypeDynamic
	c.handleProtocolMessage(chunk.Header{MessageTypeID: message.TypeSetPeerBandwidth}, bwBody)
}
