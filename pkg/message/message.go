// =============================================================================
// RTMP メッセージ層
// =============================================================================
//
// 【メッセージ層の役割】
// RTMPの上位層であり、プロトコル制御・コマンド・音声・映像などの
// 具体的なメッセージを定義する。チャンク層がトランスポートを担当し、
// メッセージ層がアプリケーションロジックを担当する。
//
// 【メッセージの分類】
//
// 1. プロトコル制御メッセージ（TypeID 1〜6）
//    - チャンクストリーム層の動作を制御する低レベルメッセージ
//    - csid=2, messageStreamID=0 で送る決まり
//
//    TypeID 1: Set Chunk Size
//      → チャンクの最大サイズを変更する
//      → 初期値128バイトだが、映像配信では4096以上に上げることが多い
//
//    TypeID 2: Abort Message
//      → 指定したチャンクストリームの未完成メッセージを破棄する
//
//    TypeID 3: Acknowledgement
//      → 受信バイト数の確認応答（フロー制御用）
//
//    TypeID 5: Window Acknowledgement Size
//      → Acknowledgementを送る間隔（バイト数）を設定
//
//    TypeID 6: Set Peer Bandwidth
//      → 相手の送信帯域幅の上限を設定
//
// 2. コマンドメッセージ（TypeID 20）
//    - AMF0でエンコードされたRPCスタイルのコマンド
//    - connect, createStream, publish, play, deleteStream など
//    - トランザクションIDで要求と応答を対応付ける
//
// 3. データメッセージ（TypeID 18）
//    - AMF0でエンコードされたメタデータ
//    - @setDataFrame, onMetaData など
//
// 4. 音声メッセージ（TypeID 8）
//    - 音声データ（AAC, MP3など）
//    - 先頭1バイトが音声ヘッダ（コーデック種別、サンプルレート等）
//
// 5. 映像メッセージ（TypeID 9）
//    - 映像データ（H.264/AVCなど）
//    - 先頭1バイトが映像ヘッダ（フレーム種別、コーデック種別）
//
// 6. ユーザー制御メッセージ（TypeID 4）
//    - ストリームの開始/終了/バッファサイズなどのイベント通知
//
package message

import (
	"encoding/binary"

	"github.com/user/rmtp/pkg/amf0"
	"github.com/user/rmtp/pkg/chunk"
)

// =============================================================================
// メッセージタイプID定数
// =============================================================================
// RTMPメッセージの種別を示す。チャンクヘッダの MessageTypeID フィールドに格納される。
const (
	// --- プロトコル制御メッセージ ---
	TypeSetChunkSize           = 1 // チャンクサイズ変更（4バイト、上位1ビットは0）
	TypeAbortMessage           = 2 // チャンクストリーム中断（4バイト = csid）
	TypeAcknowledgement        = 3 // 受信確認応答（4バイト = 受信バイト数）
	TypeUserControlMessage     = 4 // ユーザー制御イベント（可変長）
	TypeWindowAckSize          = 5 // ウィンドウ確認応答サイズ（4バイト）
	TypeSetPeerBandwidth       = 6 // ピア帯域幅設定（5バイト = サイズ + 制限タイプ）

	// --- メディア・コマンドメッセージ ---
	TypeAudioMessage           = 8  // 音声データ
	TypeVideoMessage           = 9  // 映像データ
	TypeDataMessageAMF0        = 18 // データメッセージ（AMF0エンコード）
	TypeCommandMessageAMF0     = 20 // コマンドメッセージ（AMF0エンコード）
)

// =============================================================================
// チャンクストリームID定数
// =============================================================================
// RTMPではメッセージの種類に応じて使うチャンクストリームIDが慣例的に決まっている。
const (
	CSIDProtocolControl = 2 // プロトコル制御メッセージ用（TypeID 1〜6）
	CSIDCommand         = 3 // コマンドメッセージ用（connect, publishなど）
	CSIDAudio           = 4 // 音声データ用
	CSIDVideo           = 6 // 映像データ用
)

// =============================================================================
// ユーザー制御イベント定数
// =============================================================================
// TypeUserControlMessage (TypeID=4) で使うイベントタイプ。
const (
	EventStreamBegin      = 0 // ストリーム開始（サーバーがクライアントに通知）
	EventStreamEOF        = 1 // ストリーム終了
	EventStreamDry        = 2 // ストリームデータなし
	EventSetBufferLength  = 3 // バッファ長設定
	EventStreamIsRecorded = 4 // ストリームが録画されている
	EventPingRequest      = 6 // Ping要求（キープアライブ）
	EventPingResponse     = 7 // Ping応答
)

// =============================================================================
// Set Peer Bandwidth の制限タイプ
// =============================================================================
const (
	LimitTypeHard    = 0 // ハード制限: 指定値を超えてはならない
	LimitTypeSoft    = 1 // ソフト制限: 現在の値と指定値の小さい方
	LimitTypeDynamic = 2 // 動的制限: 前回がハードならソフトとして扱う
)

// =============================================================================
// メッセージ構築ヘルパー関数群
// =============================================================================
// 各種RTMPメッセージのバイナリデータとヘッダを構築する関数。
// サーバーとクライアントの両方から使われる。

// NewSetChunkSize はSet Chunk Sizeメッセージを構築する。
// チャンクサイズを変更する際に送信する。
// データ形式: [4バイト、ビッグエンディアン、最上位ビットは0]
func NewSetChunkSize(size uint32) (chunk.Header, []byte) {
	hdr := chunk.Header{
		ChunkStreamID:   CSIDProtocolControl,
		Timestamp:       0,
		MessageTypeID:   TypeSetChunkSize,
		MessageStreamID: 0, // プロトコル制御は常にストリームID=0
	}
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, size&0x7FFFFFFF) // 最上位ビットをクリア
	return hdr, body
}

// NewWindowAckSize はWindow Acknowledgement Sizeメッセージを構築する。
// 相手に「このバイト数ごとにAcknowledgementを返してね」と伝える。
func NewWindowAckSize(size uint32) (chunk.Header, []byte) {
	hdr := chunk.Header{
		ChunkStreamID:   CSIDProtocolControl,
		Timestamp:       0,
		MessageTypeID:   TypeWindowAckSize,
		MessageStreamID: 0,
	}
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, size)
	return hdr, body
}

// NewSetPeerBandwidth はSet Peer Bandwidthメッセージを構築する。
// 相手の送信帯域幅の上限を設定する。
// データ形式: [4バイト=帯域幅] [1バイト=制限タイプ]
func NewSetPeerBandwidth(size uint32, limitType byte) (chunk.Header, []byte) {
	hdr := chunk.Header{
		ChunkStreamID:   CSIDProtocolControl,
		Timestamp:       0,
		MessageTypeID:   TypeSetPeerBandwidth,
		MessageStreamID: 0,
	}
	body := make([]byte, 5)
	binary.BigEndian.PutUint32(body[0:4], size)
	body[4] = limitType
	return hdr, body
}

// NewUserControlMessage はユーザー制御メッセージを構築する。
// データ形式: [2バイト=イベントタイプ] [イベントデータ...]
func NewUserControlMessage(eventType uint16, eventData []byte) (chunk.Header, []byte) {
	hdr := chunk.Header{
		ChunkStreamID:   CSIDProtocolControl,
		Timestamp:       0,
		MessageTypeID:   TypeUserControlMessage,
		MessageStreamID: 0,
	}
	body := make([]byte, 2+len(eventData))
	binary.BigEndian.PutUint16(body[0:2], eventType)
	copy(body[2:], eventData)
	return hdr, body
}

// NewStreamBegin はStream Beginイベント（ユーザー制御メッセージ）を構築する。
// サーバーがクライアントに「ストリームの準備ができた」ことを通知する。
func NewStreamBegin(streamID uint32) (chunk.Header, []byte) {
	eventData := make([]byte, 4)
	binary.BigEndian.PutUint32(eventData, streamID)
	return NewUserControlMessage(EventStreamBegin, eventData)
}

// =============================================================================
// AMF0コマンドメッセージ構築関数群
// =============================================================================

// NewCommandMessage はAMF0コマンドメッセージを構築する。
// RTMPのコマンドは以下の形式で送られる:
//   1. String: コマンド名（"connect", "_result", "onStatus"など）
//   2. Number: トランザクションID（要求と応答の対応付け）
//   3. 以降: コマンド固有のパラメータ（Object, Null, Stringなど）
func NewCommandMessage(streamID uint32, values ...amf0.Value) (chunk.Header, []byte, error) {
	body, err := amf0.Encode(values...)
	if err != nil {
		return chunk.Header{}, nil, err
	}
	hdr := chunk.Header{
		ChunkStreamID:   CSIDCommand,
		Timestamp:       0,
		MessageTypeID:   TypeCommandMessageAMF0,
		MessageStreamID: streamID,
	}
	return hdr, body, nil
}

// NewDataMessage はAMF0データメッセージを構築する。
// 主にメタデータ（onMetaDataなど）の送信に使う。
func NewDataMessage(streamID uint32, timestamp uint32, values ...amf0.Value) (chunk.Header, []byte, error) {
	body, err := amf0.Encode(values...)
	if err != nil {
		return chunk.Header{}, nil, err
	}
	hdr := chunk.Header{
		ChunkStreamID:   CSIDCommand,
		Timestamp:       timestamp,
		MessageTypeID:   TypeDataMessageAMF0,
		MessageStreamID: streamID,
	}
	return hdr, body, nil
}

// NewAudioMessage は音声メッセージのヘッダを構築する。
// body（音声データ）は呼び出し側が用意する。
func NewAudioMessage(streamID uint32, timestamp uint32, body []byte) (chunk.Header, []byte) {
	hdr := chunk.Header{
		ChunkStreamID:   CSIDAudio,
		Timestamp:       timestamp,
		MessageTypeID:   TypeAudioMessage,
		MessageStreamID: streamID,
	}
	return hdr, body
}

// NewVideoMessage は映像メッセージのヘッダを構築する。
// body（映像データ）は呼び出し側が用意する。
func NewVideoMessage(streamID uint32, timestamp uint32, body []byte) (chunk.Header, []byte) {
	hdr := chunk.Header{
		ChunkStreamID:   CSIDVideo,
		Timestamp:       timestamp,
		MessageTypeID:   TypeVideoMessage,
		MessageStreamID: streamID,
	}
	return hdr, body
}
