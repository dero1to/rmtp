// =============================================================================
// RTMP サーバー（受信側）
// =============================================================================
//
// 【RTMPサーバーの役割】
// RTMPサーバーはクライアント（配信ソフト）からの接続を受け付け、
// 音声・映像ストリームを受信する。受信したデータはFLVファイルとして
// 保存したり、他のクライアントに再配信（リレー）したりする。
//
// 【接続確立からストリーム配信までの全体の流れ】
//
//   クライアント                           サーバー
//       |                                    |
//       |  1. TCP接続                         |
//       |===================================>|
//       |                                    |
//       |  2. RTMP ハンドシェイク             |
//       |<==================================>|
//       |                                    |
//       |  3. connect("live")                |  ← アプリケーション名を指定
//       |===================================>|
//       |  4. Window Ack Size                |
//       |<===================================|
//       |  5. Set Peer Bandwidth             |
//       |<===================================|
//       |  6. Set Chunk Size                 |  ← チャンクサイズを4096に変更
//       |<===================================|
//       |  7. _result (connect成功)           |
//       |<===================================|
//       |                                    |
//       |  8. releaseStream("streamkey")     |  ← ストリームキーの予約解放
//       |===================================>|
//       |  9. FCPublish("streamkey")         |  ← 配信の事前通知
//       |===================================>|
//       | 10. createStream                   |  ← メッセージストリームIDの取得
//       |===================================>|
//       | 11. _result (streamID=1)           |
//       |<===================================|
//       |                                    |
//       | 12. publish("streamkey", "live")   |  ← 配信開始宣言
//       |===================================>|
//       | 13. Stream Begin                   |
//       |<===================================|
//       | 14. onStatus("Publishing")         |
//       |<===================================|
//       |                                    |
//       | 15. @setDataFrame (メタデータ)      |  ← 映像/音声の情報
//       |===================================>|
//       | 16. 音声データ (TypeID=8)          |  ← 連続送信
//       |===================================>|
//       | 17. 映像データ (TypeID=9)          |  ← 連続送信
//       |===================================>|
//       | ...繰り返し...                      |
//       |                                    |
//       | N. deleteStream / FCUnpublish      |  ← 配信終了
//       |===================================>|
//
// 【視聴（play）の流れ】
//
//   視聴クライアント                        サーバー
//       |                                    |
//       |  (TCP接続 + ハンドシェイク)         |
//       |<==================================>|
//       |  connect("live")                   |
//       |===================================>|
//       |  _result (connect成功)              |
//       |<===================================|
//       |  createStream                      |
//       |===================================>|
//       |  _result (streamID=1)              |
//       |<===================================|
//       |  play("streamkey")                 |  ← 視聴開始
//       |===================================>|
//       |  Stream Begin                      |
//       |<===================================|
//       |  onStatus("Play.Start")            |
//       |<===================================|
//       |  音声/映像データの転送              |
//       |<===================================|  ← 配信者のデータを中継
//
package server

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/user/rmtp/pkg/amf0"
	"github.com/user/rmtp/pkg/chunk"
	"github.com/user/rmtp/pkg/flv"
	"github.com/user/rmtp/pkg/handshake"
	"github.com/user/rmtp/pkg/message"
)

// =============================================================================
// サーバー設定
// =============================================================================

// Config はRTMPサーバーの設定を保持する。
type Config struct {
	Addr    string // リッスンアドレス（例: ":1935"）
	SaveDir string // FLVファイルの保存ディレクトリ（空の場合は保存しない）
}

// =============================================================================
// ストリーム管理
// =============================================================================

// stream は1つの配信ストリームの状態を管理する。
// 配信者（publisher）と視聴者（subscriber）の情報を保持する。
type stream struct {
	mu          sync.RWMutex
	publisher   *conn                     // 配信者の接続
	subscribers map[*conn]struct{}         // 視聴者の接続一覧
	metadata    []byte                     // メタデータ（onMetaData）
	audioHeader []byte                     // 音声ヘッダ（AACのSequence Header）
	videoHeader []byte                     // 映像ヘッダ（AVCのSequence Header）
}

// =============================================================================
// サーバー本体
// =============================================================================

// Server はRTMPサーバーの本体。
type Server struct {
	config  Config
	streams map[string]*stream // ストリームキー → ストリーム情報
	mu      sync.RWMutex
}

// New は新しいRTMPサーバーを作成する。
func New(config Config) *Server {
	return &Server{
		config:  config,
		streams: make(map[string]*stream),
	}
}

// ListenAndServe はRTMPサーバーを起動し、接続を待ち受ける。
// RTMPのデフォルトポートは1935。
func (s *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", s.config.Addr)
	if err != nil {
		return fmt.Errorf("サーバー起動失敗: %w", err)
	}
	defer listener.Close()

	log.Printf("[サーバー] RTMPサーバーを起動しました: %s", s.config.Addr)

	for {
		netConn, err := listener.Accept()
		if err != nil {
			log.Printf("[サーバー] 接続受付エラー: %v", err)
			continue
		}
		log.Printf("[サーバー] 新しい接続: %s", netConn.RemoteAddr())

		// 各接続をgoroutineで並行処理
		go s.handleConnection(netConn)
	}
}

// =============================================================================
// 接続管理
// =============================================================================

// conn は1つのRTMPクライアント接続を表す。
type conn struct {
	netConn     net.Conn
	reader      *chunk.Reader
	writer      *chunk.Writer
	server      *Server
	app         string // アプリケーション名（URLパスの最初の部分）
	streamKey   string // ストリームキー
	streamID    uint32 // 割り当てられたメッセージストリームID
	publishing  bool   // 配信中かどうか
	playing     bool   // 視聴中かどうか
	flvWriter   *flv.Writer
	flvFile     *os.File
}

// handleConnection は1つのクライアント接続を処理する。
func (s *Server) handleConnection(netConn net.Conn) {
	defer netConn.Close()

	c := &conn{
		netConn: netConn,
		server:  s,
	}

	// -----------------------------------------------------------------------
	// ステップ1: ハンドシェイク
	// -----------------------------------------------------------------------
	log.Printf("[%s] ハンドシェイク開始", netConn.RemoteAddr())
	if err := handshake.ServerHandshake(netConn); err != nil {
		log.Printf("[%s] ハンドシェイク失敗: %v", netConn.RemoteAddr(), err)
		return
	}
	log.Printf("[%s] ハンドシェイク完了", netConn.RemoteAddr())

	// チャンクリーダー/ライターを初期化
	c.reader = chunk.NewReader(netConn)
	c.writer = chunk.NewWriter(netConn)

	// -----------------------------------------------------------------------
	// ステップ2: メッセージループ（メッセージを受信し続ける）
	// -----------------------------------------------------------------------
	for {
		hdr, body, err := c.reader.ReadMessage()
		if err != nil {
			log.Printf("[%s] メッセージ読み取り終了: %v", netConn.RemoteAddr(), err)
			// 切断時のクリーンアップ
			c.cleanup()
			return
		}

		if err := c.handleMessage(hdr, body); err != nil {
			log.Printf("[%s] メッセージ処理エラー: %v", netConn.RemoteAddr(), err)
			c.cleanup()
			return
		}
	}
}

// cleanup は接続終了時のクリーンアップ処理を行う。
func (c *conn) cleanup() {
	// FLVファイルを閉じる
	if c.flvFile != nil {
		c.flvFile.Close()
		c.flvFile = nil
	}

	// ストリームから配信者/視聴者を削除
	if c.streamKey != "" {
		c.server.mu.Lock()
		if s, ok := c.server.streams[c.streamKey]; ok {
			s.mu.Lock()
			if c.publishing {
				s.publisher = nil
				// 視聴者全員に配信終了を通知（簡易実装のためストリーム削除）
				delete(c.server.streams, c.streamKey)
			} else {
				delete(s.subscribers, c)
			}
			s.mu.Unlock()
		}
		c.server.mu.Unlock()
	}
}

// =============================================================================
// メッセージ処理
// =============================================================================

// handleMessage は受信したメッセージをタイプに応じて処理する。
func (c *conn) handleMessage(hdr chunk.Header, body []byte) error {
	switch hdr.MessageTypeID {
	case message.TypeSetChunkSize:
		// Set Chunk Size: クライアントがチャンクサイズを変更した
		return c.handleSetChunkSize(body)

	case message.TypeWindowAckSize:
		// Window Acknowledgement Size: フロー制御（受信するだけ）
		return nil

	case message.TypeCommandMessageAMF0:
		// AMF0コマンドメッセージ: connect, publish, playなど
		return c.handleCommandMessage(hdr, body)

	case message.TypeDataMessageAMF0:
		// AMF0データメッセージ: メタデータなど
		return c.handleDataMessage(hdr, body)

	case message.TypeAudioMessage:
		// 音声メッセージ
		return c.handleMediaMessage(hdr, body)

	case message.TypeVideoMessage:
		// 映像メッセージ
		return c.handleMediaMessage(hdr, body)

	case message.TypeAcknowledgement:
		// Acknowledgement: フロー制御（受信するだけ）
		return nil

	case message.TypeUserControlMessage:
		// ユーザー制御メッセージ（受信するだけ）
		return nil

	case message.TypeAbortMessage:
		// Abort Message（受信するだけ）
		return nil

	default:
		log.Printf("[%s] 未処理のメッセージタイプ: %d", c.netConn.RemoteAddr(), hdr.MessageTypeID)
		return nil
	}
}

// handleSetChunkSize はSet Chunk Sizeメッセージを処理する。
// クライアントが「これ以降は指定サイズでチャンクを送る」ことを通知してきた。
func (c *conn) handleSetChunkSize(body []byte) error {
	if len(body) < 4 {
		return fmt.Errorf("Set Chunk Size: データが短すぎる")
	}
	newSize := binary.BigEndian.Uint32(body) & 0x7FFFFFFF
	log.Printf("[%s] チャンクサイズ変更: %d", c.netConn.RemoteAddr(), newSize)
	c.reader.SetChunkSize(newSize)
	return nil
}

// =============================================================================
// コマンドメッセージ処理
// =============================================================================
// RTMPのコマンドはAMF0でエンコードされた値の配列として送られる。
// 最初の値がコマンド名（文字列）、2番目がトランザクションID（数値）。

// handleCommandMessage はAMF0コマンドメッセージを処理する。
func (c *conn) handleCommandMessage(hdr chunk.Header, body []byte) error {
	// AMF0をデコード
	values, err := amf0.Decode(body)
	if err != nil {
		return fmt.Errorf("AMF0デコードエラー: %w", err)
	}
	if len(values) < 2 {
		return fmt.Errorf("コマンドメッセージの値が不足")
	}

	// コマンド名を取得
	cmdName, ok := values[0].(string)
	if !ok {
		return fmt.Errorf("コマンド名が文字列ではない")
	}

	log.Printf("[%s] コマンド受信: %s", c.netConn.RemoteAddr(), cmdName)

	switch cmdName {
	case "connect":
		return c.handleConnect(values)
	case "releaseStream":
		// releaseStream: 以前のストリームを解放する（応答不要の場合が多い）
		return c.sendResultOK(values)
	case "FCPublish":
		// FCPublish: 配信の事前通知（応答不要の場合が多い）
		return c.sendResultOK(values)
	case "createStream":
		return c.handleCreateStream(values)
	case "publish":
		return c.handlePublish(hdr, values)
	case "play":
		return c.handlePlay(hdr, values)
	case "deleteStream":
		return c.handleDeleteStream(values)
	case "FCUnpublish":
		// FCUnpublish: 配信終了通知
		return nil
	default:
		log.Printf("[%s] 未処理のコマンド: %s", c.netConn.RemoteAddr(), cmdName)
		return nil
	}
}

// handleConnect はconnectコマンドを処理する。
//
// 【connectコマンドの役割】
// クライアントがサーバーに「このアプリケーションに接続したい」と要求する。
// アプリケーション名（通常は"live"）はRTMP URLのパス部分に対応する。
// 例: rtmp://server/live/streamkey → app="live"
//
// 【サーバーの応答】
// 1. Window Acknowledgement Size を送信
// 2. Set Peer Bandwidth を送信
// 3. Set Chunk Size を送信（チャンクサイズを4096に変更）
// 4. _result を送信（connect成功を通知）
func (c *conn) handleConnect(values []amf0.Value) error {
	// connectコマンドのプロパティオブジェクトからapp名を取得
	if len(values) >= 3 {
		if obj, ok := values[2].(*amf0.Object); ok {
			if app, exists := obj.Get("app"); exists {
				if appStr, ok := app.(string); ok {
					c.app = appStr
					log.Printf("[%s] アプリケーション: %s", c.netConn.RemoteAddr(), c.app)
				}
			}
		}
	}

	// --- Window Acknowledgement Size を送信 ---
	// 「2500000バイト受信するごとにAckを返してね」
	ackHdr, ackBody := message.NewWindowAckSize(2500000)
	if err := c.writer.WriteMessage(ackHdr, ackBody); err != nil {
		return err
	}

	// --- Set Peer Bandwidth を送信 ---
	// 「クライアントの送信帯域幅を2500000バイト/秒に制限」
	bwHdr, bwBody := message.NewSetPeerBandwidth(2500000, message.LimitTypeDynamic)
	if err := c.writer.WriteMessage(bwHdr, bwBody); err != nil {
		return err
	}

	// --- Set Chunk Size を送信 ---
	// チャンクサイズを4096バイトに変更（デフォルト128では非効率）
	csHdr, csBody := message.NewSetChunkSize(4096)
	if err := c.writer.WriteMessage(csHdr, csBody); err != nil {
		return err
	}
	c.writer.SetChunkSize(4096)

	// --- _result を送信（connect成功） ---
	// トランザクションIDは受信したconnectコマンドと同じ値を使う
	txnID := float64(1)
	if len(values) >= 2 {
		if id, ok := values[1].(float64); ok {
			txnID = id
		}
	}

	// サーバーのプロパティオブジェクト
	props := amf0.NewObject()
	props.Set("fmsVer", "FMS/3,0,1,123")       // Flash Media Serverのバージョン（互換性のため）
	props.Set("capabilities", float64(31))       // サーバーの機能フラグ

	// 接続結果の情報オブジェクト
	info := amf0.NewObject()
	info.Set("level", "status")
	info.Set("code", "NetConnection.Connect.Success")
	info.Set("description", "Connection succeeded.")
	info.Set("objectEncoding", float64(0)) // AMF0を使用

	resultHdr, resultBody, err := message.NewCommandMessage(0,
		"_result", txnID, props, info,
	)
	if err != nil {
		return err
	}
	return c.writer.WriteMessage(resultHdr, resultBody)
}

// handleCreateStream はcreateStreamコマンドを処理する。
//
// 【createStreamの役割】
// クライアントが新しいメッセージストリームIDの割り当てを要求する。
// このIDは音声・映像メッセージのMessageStreamIDとして使われる。
// サーバーは通常1を返す。
func (c *conn) handleCreateStream(values []amf0.Value) error {
	txnID := float64(0)
	if len(values) >= 2 {
		if id, ok := values[1].(float64); ok {
			txnID = id
		}
	}

	// メッセージストリームID=1を割り当てる
	c.streamID = 1

	// _result で割り当てたストリームIDを返す
	resultHdr, resultBody, err := message.NewCommandMessage(0,
		"_result", txnID, nil, float64(c.streamID),
	)
	if err != nil {
		return err
	}
	return c.writer.WriteMessage(resultHdr, resultBody)
}

// handlePublish はpublishコマンドを処理する。
//
// 【publishの役割】
// クライアントが「このストリームキーで配信を開始する」と宣言する。
// サーバーは配信を許可し、以降の音声/映像メッセージを受け付ける。
//
// publishコマンドのパラメータ:
//   values[0] = "publish" （コマンド名）
//   values[1] = トランザクションID
//   values[2] = null
//   values[3] = ストリームキー（文字列）
//   values[4] = 配信タイプ（"live", "record", "append"）
func (c *conn) handlePublish(hdr chunk.Header, values []amf0.Value) error {
	// ストリームキーを取得
	if len(values) >= 4 {
		if key, ok := values[3].(string); ok {
			c.streamKey = key
		}
	}
	if c.streamKey == "" {
		c.streamKey = "default"
	}

	log.Printf("[%s] 配信開始: app=%s, key=%s", c.netConn.RemoteAddr(), c.app, c.streamKey)

	// ストリームを登録
	c.server.mu.Lock()
	s, ok := c.server.streams[c.streamKey]
	if !ok {
		s = &stream{
			subscribers: make(map[*conn]struct{}),
		}
		c.server.streams[c.streamKey] = s
	}
	s.mu.Lock()
	s.publisher = c
	s.mu.Unlock()
	c.server.mu.Unlock()

	c.publishing = true

	// FLVファイル保存の設定
	if c.server.config.SaveDir != "" {
		if err := c.initFLVWriter(); err != nil {
			log.Printf("[%s] FLVファイル作成エラー: %v", c.netConn.RemoteAddr(), err)
		}
	}

	// --- Stream Begin を送信 ---
	sbHdr, sbBody := message.NewStreamBegin(c.streamID)
	if err := c.writer.WriteMessage(sbHdr, sbBody); err != nil {
		return err
	}

	// --- onStatus を送信（配信開始を通知） ---
	status := amf0.NewObject()
	status.Set("level", "status")
	status.Set("code", "NetStream.Publish.Start")
	status.Set("description", "Publishing started.")

	statusHdr, statusBody, err := message.NewCommandMessage(c.streamID,
		"onStatus", float64(0), nil, status,
	)
	if err != nil {
		return err
	}
	return c.writer.WriteMessage(statusHdr, statusBody)
}

// handlePlay はplayコマンドを処理する。
//
// 【playの役割】
// クライアントが「このストリームキーの配信を視聴したい」と要求する。
// サーバーは視聴者リストに追加し、配信者からの音声/映像を中継する。
func (c *conn) handlePlay(hdr chunk.Header, values []amf0.Value) error {
	// ストリームキーを取得
	if len(values) >= 4 {
		if key, ok := values[3].(string); ok {
			c.streamKey = key
		}
	}
	if c.streamKey == "" {
		c.streamKey = "default"
	}

	log.Printf("[%s] 視聴開始: app=%s, key=%s", c.netConn.RemoteAddr(), c.app, c.streamKey)

	// ストリームの視聴者リストに追加
	c.server.mu.Lock()
	s, ok := c.server.streams[c.streamKey]
	if !ok {
		s = &stream{
			subscribers: make(map[*conn]struct{}),
		}
		c.server.streams[c.streamKey] = s
	}
	s.mu.Lock()
	s.subscribers[c] = struct{}{}
	s.mu.Unlock()
	c.server.mu.Unlock()

	c.playing = true

	// --- Stream Begin を送信 ---
	sbHdr, sbBody := message.NewStreamBegin(c.streamID)
	if err := c.writer.WriteMessage(sbHdr, sbBody); err != nil {
		return err
	}

	// --- onStatus を送信（視聴開始を通知） ---
	status := amf0.NewObject()
	status.Set("level", "status")
	status.Set("code", "NetStream.Play.Start")
	status.Set("description", "Playing started.")

	statusHdr, statusBody, err := message.NewCommandMessage(c.streamID,
		"onStatus", float64(0), nil, status,
	)
	if err != nil {
		return err
	}
	if err := c.writer.WriteMessage(statusHdr, statusBody); err != nil {
		return err
	}

	// 配信者がいる場合、ヘッダ情報を送信（視聴者が途中参加できるように）
	c.server.mu.RLock()
	if s, ok := c.server.streams[c.streamKey]; ok {
		s.mu.RLock()
		// メタデータを送信
		if s.metadata != nil {
			metaHdr := chunk.Header{
				ChunkStreamID:   message.CSIDCommand,
				Timestamp:       0,
				MessageTypeID:   message.TypeDataMessageAMF0,
				MessageStreamID: c.streamID,
			}
			_ = c.writer.WriteMessage(metaHdr, s.metadata)
		}
		// 音声ヘッダを送信（AACのDecoder Specific Configなど）
		if s.audioHeader != nil {
			audioHdr := chunk.Header{
				ChunkStreamID:   message.CSIDAudio,
				Timestamp:       0,
				MessageTypeID:   message.TypeAudioMessage,
				MessageStreamID: c.streamID,
			}
			_ = c.writer.WriteMessage(audioHdr, s.audioHeader)
		}
		// 映像ヘッダを送信（AVCのSequence Headerなど）
		if s.videoHeader != nil {
			videoHdr := chunk.Header{
				ChunkStreamID:   message.CSIDVideo,
				Timestamp:       0,
				MessageTypeID:   message.TypeVideoMessage,
				MessageStreamID: c.streamID,
			}
			_ = c.writer.WriteMessage(videoHdr, s.videoHeader)
		}
		s.mu.RUnlock()
	}
	c.server.mu.RUnlock()

	return nil
}

// handleDeleteStream はdeleteStreamコマンドを処理する。
func (c *conn) handleDeleteStream(values []amf0.Value) error {
	log.Printf("[%s] ストリーム削除", c.netConn.RemoteAddr())
	c.cleanup()
	return nil
}

// =============================================================================
// データメッセージ処理
// =============================================================================

// handleDataMessage はAMF0データメッセージを処理する。
// 主に @setDataFrame / onMetaData を受信する。
//
// 【onMetaDataの役割】
// 配信者が送信する映像/音声のメタ情報。以下のような情報を含む:
//   - duration: 長さ（ライブ配信では0）
//   - width/height: 映像の解像度
//   - videodatarate: 映像ビットレート
//   - audiodatarate: 音声ビットレート
//   - videocodecid: 映像コーデック（7=H.264/AVC）
//   - audiocodecid: 音声コーデック（10=AAC）
//   - framerate: フレームレート
func (c *conn) handleDataMessage(hdr chunk.Header, body []byte) error {
	values, err := amf0.Decode(body)
	if err != nil {
		return nil // デコードエラーは無視
	}

	if len(values) > 0 {
		if name, ok := values[0].(string); ok {
			log.Printf("[%s] データメッセージ: %s", c.netConn.RemoteAddr(), name)
		}
	}

	// メタデータをストリームに保存（視聴者の途中参加用）
	if c.streamKey != "" {
		c.server.mu.RLock()
		if s, ok := c.server.streams[c.streamKey]; ok {
			s.mu.Lock()
			// @setDataFrame の場合、最初の値を除いた残りがメタデータ本体
			if len(values) > 0 {
				if name, ok := values[0].(string); ok && name == "@setDataFrame" {
					// @setDataFrame を除いたメタデータを保存
					if len(values) > 1 {
						metaBody, _ := amf0.Encode(values[1:]...)
						s.metadata = metaBody
					}
				} else {
					s.metadata = body
				}
			}
			s.mu.Unlock()
		}
		c.server.mu.RUnlock()
	}

	// FLVに書き込む
	if c.flvWriter != nil {
		c.flvWriter.WriteTag(flv.TagTypeScript, hdr.Timestamp, body)
	}

	// 視聴者にメタデータを転送
	c.relayToSubscribers(hdr, body)

	return nil
}

// =============================================================================
// メディアメッセージ処理
// =============================================================================

// handleMediaMessage は音声/映像メッセージを処理する。
//
// 【音声メッセージ（TypeID=8）の構造】
// 先頭1バイトが音声タグヘッダ:
//   - 上位4ビット: コーデック種別
//     - 2=MP3, 10=AAC, 11=Speex
//   - ビット3-2: サンプルレート
//     - 0=5.5kHz, 1=11kHz, 2=22kHz, 3=44kHz
//   - ビット1: サンプルサイズ（0=8bit, 1=16bit）
//   - ビット0: チャンネル（0=モノラル, 1=ステレオ）
//
// AACの場合、2バイト目が:
//   - 0=AAC Sequence Header（デコーダ設定情報）
//   - 1=AAC Raw（実際の音声データ）
//
// 【映像メッセージ（TypeID=9）の構造】
// 先頭1バイトが映像タグヘッダ:
//   - 上位4ビット: フレームタイプ
//     - 1=キーフレーム（I-frame）
//     - 2=インターフレーム（P-frame）
//   - 下位4ビット: コーデック種別
//     - 7=H.264/AVC
//
// H.264/AVCの場合、2バイト目が:
//   - 0=AVC Sequence Header（SPS/PPS情報）
//   - 1=AVC NALU（実際の映像データ）
//   - 2=AVC End of Sequence
func (c *conn) handleMediaMessage(hdr chunk.Header, body []byte) error {
	if len(body) == 0 {
		return nil
	}

	// ヘッダ情報を保存（視聴者の途中参加用）
	if c.streamKey != "" {
		c.server.mu.RLock()
		if s, ok := c.server.streams[c.streamKey]; ok {
			s.mu.Lock()
			switch hdr.MessageTypeID {
			case message.TypeAudioMessage:
				// AACのSequence Headerかチェック
				if len(body) >= 2 && (body[0]>>4) == 10 && body[1] == 0 {
					s.audioHeader = make([]byte, len(body))
					copy(s.audioHeader, body)
				}
			case message.TypeVideoMessage:
				// AVCのSequence Headerかチェック
				if len(body) >= 2 && (body[0]&0x0F) == 7 && body[1] == 0 {
					s.videoHeader = make([]byte, len(body))
					copy(s.videoHeader, body)
				}
			}
			s.mu.Unlock()
		}
		c.server.mu.RUnlock()
	}

	// FLVに書き込む
	if c.flvWriter != nil {
		tagType := byte(flv.TagTypeAudio)
		if hdr.MessageTypeID == message.TypeVideoMessage {
			tagType = flv.TagTypeVideo
		}
		c.flvWriter.WriteTag(tagType, hdr.Timestamp, body)
	}

	// 視聴者に中継
	c.relayToSubscribers(hdr, body)

	return nil
}

// relayToSubscribers は配信者からのデータを全視聴者に中継する。
func (c *conn) relayToSubscribers(hdr chunk.Header, body []byte) {
	if c.streamKey == "" {
		return
	}

	c.server.mu.RLock()
	s, ok := c.server.streams[c.streamKey]
	c.server.mu.RUnlock()
	if !ok {
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for sub := range s.subscribers {
		// 視聴者のストリームIDでメッセージを転送
		relayHdr := hdr
		relayHdr.MessageStreamID = sub.streamID
		if err := sub.writer.WriteMessage(relayHdr, body); err != nil {
			log.Printf("[%s] 視聴者への中継エラー: %v", sub.netConn.RemoteAddr(), err)
		}
	}
}

// =============================================================================
// ヘルパー関数
// =============================================================================

// sendResultOK はシンプルな_resultレスポンスを送信する。
func (c *conn) sendResultOK(values []amf0.Value) error {
	txnID := float64(0)
	if len(values) >= 2 {
		if id, ok := values[1].(float64); ok {
			txnID = id
		}
	}
	resultHdr, resultBody, err := message.NewCommandMessage(0,
		"_result", txnID, nil, nil,
	)
	if err != nil {
		return err
	}
	return c.writer.WriteMessage(resultHdr, resultBody)
}

// initFLVWriter はFLVファイルライターを初期化する。
func (c *conn) initFLVWriter() error {
	// 保存ディレクトリが存在しなければ作成
	if err := os.MkdirAll(c.server.config.SaveDir, 0755); err != nil {
		return err
	}

	// ファイル名: ストリームキーをサニタイズして使用
	safeKey := strings.ReplaceAll(c.streamKey, "/", "_")
	safeKey = strings.ReplaceAll(safeKey, "\\", "_")
	filename := filepath.Join(c.server.config.SaveDir, safeKey+".flv")

	f, err := os.Create(filename)
	if err != nil {
		return err
	}

	writer, err := flv.NewWriter(f, true, true)
	if err != nil {
		f.Close()
		return err
	}

	c.flvFile = f
	c.flvWriter = writer
	log.Printf("[%s] FLV保存開始: %s", c.netConn.RemoteAddr(), filename)
	return nil
}
