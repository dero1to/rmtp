package message

import (
	"encoding/binary"
	"testing"

	"github.com/user/rmtp/pkg/amf0"
	"github.com/user/rmtp/pkg/chunk"
)

// TestNewSetChunkSize はSet Chunk Sizeメッセージの構築をテストする。
func TestNewSetChunkSize(t *testing.T) {
	tests := []struct {
		name string
		size uint32
		want uint32
	}{
		{"デフォルト128", 128, 128},
		{"一般的な4096", 4096, 4096},
		{"最大値", 0x7FFFFFFF, 0x7FFFFFFF},
		{"最上位ビットがクリアされる", 0x80000001, 1}, // MSBがクリアされて1になる
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdr, body := NewSetChunkSize(tt.size)

			// ヘッダの検証
			if hdr.ChunkStreamID != CSIDProtocolControl {
				t.Errorf("ChunkStreamID = %d, want %d", hdr.ChunkStreamID, CSIDProtocolControl)
			}
			if hdr.MessageTypeID != TypeSetChunkSize {
				t.Errorf("MessageTypeID = %d, want %d", hdr.MessageTypeID, TypeSetChunkSize)
			}
			if hdr.MessageStreamID != 0 {
				t.Errorf("MessageStreamID = %d, want 0", hdr.MessageStreamID)
			}

			// ボディの検証
			if len(body) != 4 {
				t.Fatalf("body length = %d, want 4", len(body))
			}
			got := binary.BigEndian.Uint32(body)
			if got != tt.want {
				t.Errorf("chunk size = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestNewWindowAckSize はWindow Acknowledgement Sizeメッセージの構築をテストする。
func TestNewWindowAckSize(t *testing.T) {
	hdr, body := NewWindowAckSize(2500000)

	if hdr.ChunkStreamID != CSIDProtocolControl {
		t.Errorf("ChunkStreamID = %d, want %d", hdr.ChunkStreamID, CSIDProtocolControl)
	}
	if hdr.MessageTypeID != TypeWindowAckSize {
		t.Errorf("MessageTypeID = %d, want %d", hdr.MessageTypeID, TypeWindowAckSize)
	}
	if len(body) != 4 {
		t.Fatalf("body length = %d, want 4", len(body))
	}
	got := binary.BigEndian.Uint32(body)
	if got != 2500000 {
		t.Errorf("window ack size = %d, want 2500000", got)
	}
}

// TestNewSetPeerBandwidth はSet Peer Bandwidthメッセージの構築をテストする。
func TestNewSetPeerBandwidth(t *testing.T) {
	tests := []struct {
		name      string
		size      uint32
		limitType byte
	}{
		{"Hard", 2500000, LimitTypeHard},
		{"Soft", 1000000, LimitTypeSoft},
		{"Dynamic", 2500000, LimitTypeDynamic},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdr, body := NewSetPeerBandwidth(tt.size, tt.limitType)

			if hdr.MessageTypeID != TypeSetPeerBandwidth {
				t.Errorf("MessageTypeID = %d, want %d", hdr.MessageTypeID, TypeSetPeerBandwidth)
			}
			if len(body) != 5 {
				t.Fatalf("body length = %d, want 5", len(body))
			}
			gotSize := binary.BigEndian.Uint32(body[0:4])
			if gotSize != tt.size {
				t.Errorf("bandwidth = %d, want %d", gotSize, tt.size)
			}
			if body[4] != tt.limitType {
				t.Errorf("limit type = %d, want %d", body[4], tt.limitType)
			}
		})
	}
}

// TestNewUserControlMessage はユーザー制御メッセージの構築をテストする。
func TestNewUserControlMessage(t *testing.T) {
	eventData := []byte{0x00, 0x00, 0x00, 0x01} // streamID=1
	hdr, body := NewUserControlMessage(EventStreamBegin, eventData)

	if hdr.MessageTypeID != TypeUserControlMessage {
		t.Errorf("MessageTypeID = %d, want %d", hdr.MessageTypeID, TypeUserControlMessage)
	}
	if len(body) != 6 { // 2バイト(イベントタイプ) + 4バイト(イベントデータ)
		t.Fatalf("body length = %d, want 6", len(body))
	}
	eventType := binary.BigEndian.Uint16(body[0:2])
	if eventType != EventStreamBegin {
		t.Errorf("event type = %d, want %d", eventType, EventStreamBegin)
	}
	streamID := binary.BigEndian.Uint32(body[2:6])
	if streamID != 1 {
		t.Errorf("stream ID = %d, want 1", streamID)
	}
}

// TestNewStreamBegin はStream Beginイベントの構築をテストする。
func TestNewStreamBegin(t *testing.T) {
	hdr, body := NewStreamBegin(1)

	if hdr.MessageTypeID != TypeUserControlMessage {
		t.Errorf("MessageTypeID = %d, want %d", hdr.MessageTypeID, TypeUserControlMessage)
	}
	eventType := binary.BigEndian.Uint16(body[0:2])
	if eventType != EventStreamBegin {
		t.Errorf("event type = %d, want %d", eventType, EventStreamBegin)
	}
	streamID := binary.BigEndian.Uint32(body[2:6])
	if streamID != 1 {
		t.Errorf("stream ID = %d, want 1", streamID)
	}
}

// TestNewCommandMessage はコマンドメッセージの構築をテストする。
func TestNewCommandMessage(t *testing.T) {
	t.Run("connectコマンド", func(t *testing.T) {
		obj := amf0.NewObject()
		obj.Set("app", "live")

		hdr, body, err := NewCommandMessage(0, "connect", float64(1), obj)
		if err != nil {
			t.Fatalf("エラー: %v", err)
		}

		if hdr.ChunkStreamID != CSIDCommand {
			t.Errorf("ChunkStreamID = %d, want %d", hdr.ChunkStreamID, CSIDCommand)
		}
		if hdr.MessageTypeID != TypeCommandMessageAMF0 {
			t.Errorf("MessageTypeID = %d, want %d", hdr.MessageTypeID, TypeCommandMessageAMF0)
		}
		if hdr.MessageStreamID != 0 {
			t.Errorf("MessageStreamID = %d, want 0", hdr.MessageStreamID)
		}

		// AMF0デコードで検証
		values, err := amf0.Decode(body)
		if err != nil {
			t.Fatalf("AMF0デコードエラー: %v", err)
		}
		if len(values) != 3 {
			t.Fatalf("値の数 = %d, want 3", len(values))
		}
		if cmdName, ok := values[0].(string); !ok || cmdName != "connect" {
			t.Errorf("コマンド名 = %v, want connect", values[0])
		}
		if txnID, ok := values[1].(float64); !ok || txnID != 1.0 {
			t.Errorf("トランザクションID = %v, want 1.0", values[1])
		}
	})

	t.Run("publishコマンド", func(t *testing.T) {
		hdr, body, err := NewCommandMessage(1, "publish", float64(0), nil, "streamkey", "live")
		if err != nil {
			t.Fatalf("エラー: %v", err)
		}

		if hdr.MessageStreamID != 1 {
			t.Errorf("MessageStreamID = %d, want 1", hdr.MessageStreamID)
		}

		values, err := amf0.Decode(body)
		if err != nil {
			t.Fatalf("AMF0デコードエラー: %v", err)
		}
		if len(values) != 5 {
			t.Fatalf("値の数 = %d, want 5", len(values))
		}
		if cmdName, ok := values[0].(string); !ok || cmdName != "publish" {
			t.Errorf("コマンド名 = %v, want publish", values[0])
		}
		if key, ok := values[3].(string); !ok || key != "streamkey" {
			t.Errorf("ストリームキー = %v, want streamkey", values[3])
		}
	})
}

// TestNewDataMessage はデータメッセージの構築をテストする。
func TestNewDataMessage(t *testing.T) {
	metadata := amf0.NewObject()
	metadata.Set("width", float64(1920))
	metadata.Set("height", float64(1080))

	hdr, body, err := NewDataMessage(1, 500, "@setDataFrame", "onMetaData", metadata)
	if err != nil {
		t.Fatalf("エラー: %v", err)
	}

	if hdr.ChunkStreamID != CSIDCommand {
		t.Errorf("ChunkStreamID = %d, want %d", hdr.ChunkStreamID, CSIDCommand)
	}
	if hdr.MessageTypeID != TypeDataMessageAMF0 {
		t.Errorf("MessageTypeID = %d, want %d", hdr.MessageTypeID, TypeDataMessageAMF0)
	}
	if hdr.Timestamp != 500 {
		t.Errorf("Timestamp = %d, want 500", hdr.Timestamp)
	}
	if hdr.MessageStreamID != 1 {
		t.Errorf("MessageStreamID = %d, want 1", hdr.MessageStreamID)
	}

	values, err := amf0.Decode(body)
	if err != nil {
		t.Fatalf("AMF0デコードエラー: %v", err)
	}
	if len(values) != 3 {
		t.Fatalf("値の数 = %d, want 3", len(values))
	}
	if name, ok := values[0].(string); !ok || name != "@setDataFrame" {
		t.Errorf("値[0] = %v, want @setDataFrame", values[0])
	}
}

// TestNewAudioMessage は音声メッセージの構築をテストする。
func TestNewAudioMessage(t *testing.T) {
	audioData := []byte{0xAF, 0x01, 0x12, 0x34} // AAC raw data
	hdr, body := NewAudioMessage(1, 1000, audioData)

	if hdr.ChunkStreamID != CSIDAudio {
		t.Errorf("ChunkStreamID = %d, want %d", hdr.ChunkStreamID, CSIDAudio)
	}
	if hdr.MessageTypeID != TypeAudioMessage {
		t.Errorf("MessageTypeID = %d, want %d", hdr.MessageTypeID, TypeAudioMessage)
	}
	if hdr.Timestamp != 1000 {
		t.Errorf("Timestamp = %d, want 1000", hdr.Timestamp)
	}
	if hdr.MessageStreamID != 1 {
		t.Errorf("MessageStreamID = %d, want 1", hdr.MessageStreamID)
	}
	if len(body) != 4 {
		t.Errorf("body length = %d, want 4", len(body))
	}
}

// TestNewVideoMessage は映像メッセージの構築をテストする。
func TestNewVideoMessage(t *testing.T) {
	videoData := []byte{0x17, 0x00, 0x00, 0x00, 0x00} // AVC sequence header
	hdr, body := NewVideoMessage(1, 2000, videoData)

	if hdr.ChunkStreamID != CSIDVideo {
		t.Errorf("ChunkStreamID = %d, want %d", hdr.ChunkStreamID, CSIDVideo)
	}
	if hdr.MessageTypeID != TypeVideoMessage {
		t.Errorf("MessageTypeID = %d, want %d", hdr.MessageTypeID, TypeVideoMessage)
	}
	if hdr.Timestamp != 2000 {
		t.Errorf("Timestamp = %d, want 2000", hdr.Timestamp)
	}
	if hdr.MessageStreamID != 1 {
		t.Errorf("MessageStreamID = %d, want 1", hdr.MessageStreamID)
	}
	if len(body) != 5 {
		t.Errorf("body length = %d, want 5", len(body))
	}
}

// TestMessageHeaderConsistency はメッセージ構築関数のヘッダ整合性をテストする。
// プロトコル制御メッセージは常にcsid=2, streamID=0であること。
func TestMessageHeaderConsistency(t *testing.T) {
	protocolMessages := []struct {
		name string
		hdr  chunk.Header
	}{
		{"SetChunkSize", func() chunk.Header { h, _ := NewSetChunkSize(4096); return h }()},
		{"WindowAckSize", func() chunk.Header { h, _ := NewWindowAckSize(2500000); return h }()},
		{"SetPeerBandwidth", func() chunk.Header { h, _ := NewSetPeerBandwidth(2500000, LimitTypeDynamic); return h }()},
		{"StreamBegin", func() chunk.Header { h, _ := NewStreamBegin(1); return h }()},
	}

	for _, tt := range protocolMessages {
		t.Run(tt.name, func(t *testing.T) {
			if tt.hdr.ChunkStreamID != CSIDProtocolControl {
				t.Errorf("ChunkStreamID = %d, want %d (CSIDProtocolControl)", tt.hdr.ChunkStreamID, CSIDProtocolControl)
			}
			if tt.hdr.MessageStreamID != 0 {
				t.Errorf("MessageStreamID = %d, want 0", tt.hdr.MessageStreamID)
			}
		})
	}
}
