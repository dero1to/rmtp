// =============================================================================
// RTMP クライアント（送信側 / 配信者）
// =============================================================================
//
// 【RTMPクライアントの役割】
// FLVファイルやリアルタイムの音声/映像データをRTMPサーバーに送信する。
// OBSなどの配信ソフトと同じ役割を、このライブラリ単体で果たす。
//
// 【接続〜配信開始までのクライアント側の処理】
//
// 1. TCP接続確立
// 2. RTMPハンドシェイク（C0+C1送信 → S0+S1+S2受信 → C2送信）
// 3. connectコマンド送信（アプリケーション名指定）
// 4. サーバーからの応答待ち（Window Ack Size, Set Peer Bandwidth, _result）
// 5. createStreamコマンド送信（ストリームID取得）
// 6. publishコマンド送信（ストリームキー指定、配信開始宣言）
// 7. メタデータ送信（@setDataFrame + onMetaData）
// 8. 音声/映像データの連続送信
//
// 【RTMP URLの構造】
// rtmp://hostname:port/app/streamkey
//   - hostname: サーバーのホスト名またはIPアドレス
//   - port: ポート番号（デフォルト1935）
//   - app: アプリケーション名（通常は"live"）
//   - streamkey: ストリームキー（配信の識別子）
//
package client

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/user/rmtp/pkg/amf0"
	"github.com/user/rmtp/pkg/chunk"
	"github.com/user/rmtp/pkg/flv"
	"github.com/user/rmtp/pkg/handshake"
	"github.com/user/rmtp/pkg/message"
)

// =============================================================================
// クライアント構造体
// =============================================================================

// Client はRTMPクライアント（配信者）を表す。
type Client struct {
	conn      net.Conn
	reader    *chunk.Reader
	writer    *chunk.Writer
	app       string // アプリケーション名
	streamKey string // ストリームキー
	streamID  uint32 // サーバーから割り当てられたストリームID
	txnID     float64 // トランザクションIDカウンター
}

// =============================================================================
// 接続と配信開始
// =============================================================================

// Publish はRTMPサーバーに接続し、配信可能な状態にする。
// addr: サーバーアドレス（例: "localhost:1935"）
// app: アプリケーション名（例: "live"）
// streamKey: ストリームキー（例: "test"）
func Publish(addr, app, streamKey string) (*Client, error) {
	c := &Client{
		app:       app,
		streamKey: streamKey,
		txnID:     1,
	}

	// -----------------------------------------------------------------------
	// ステップ1: TCP接続
	// -----------------------------------------------------------------------
	log.Printf("[クライアント] %s に接続中...", addr)
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("TCP接続に失敗: %w", err)
	}
	c.conn = conn

	// -----------------------------------------------------------------------
	// ステップ2: RTMPハンドシェイク
	// -----------------------------------------------------------------------
	log.Printf("[クライアント] ハンドシェイク開始")
	if err := handshake.ClientHandshake(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ハンドシェイクに失敗: %w", err)
	}
	log.Printf("[クライアント] ハンドシェイク完了")

	c.reader = chunk.NewReader(conn)
	c.writer = chunk.NewWriter(conn)

	// -----------------------------------------------------------------------
	// ステップ3: connectコマンド送信
	// -----------------------------------------------------------------------
	if err := c.sendConnect(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect失敗: %w", err)
	}

	// -----------------------------------------------------------------------
	// ステップ4: サーバーからの応答を処理
	// -----------------------------------------------------------------------
	if err := c.readServerResponses(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("サーバー応答の処理に失敗: %w", err)
	}

	// -----------------------------------------------------------------------
	// ステップ5: createStream送信
	// -----------------------------------------------------------------------
	if err := c.sendCreateStream(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("createStream失敗: %w", err)
	}

	// createStreamの応答を読む
	if err := c.readCreateStreamResult(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("createStreamの応答処理に失敗: %w", err)
	}

	// -----------------------------------------------------------------------
	// ステップ6: publishコマンド送信
	// -----------------------------------------------------------------------
	if err := c.sendPublish(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("publish失敗: %w", err)
	}

	// publishの応答を読む（onStatus）
	if err := c.readUntilPublishStart(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("publish応答の処理に失敗: %w", err)
	}

	log.Printf("[クライアント] 配信準備完了: app=%s, key=%s, streamID=%d",
		c.app, c.streamKey, c.streamID)

	return c, nil
}

// Close はRTMP接続を閉じる。
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// =============================================================================
// コマンド送信
// =============================================================================

// sendConnect はconnectコマンドを送信する。
func (c *Client) sendConnect() error {
	// connectコマンドのプロパティオブジェクト
	obj := amf0.NewObject()
	obj.Set("app", c.app)                                  // アプリケーション名
	obj.Set("type", "nonprivate")                           // 接続タイプ
	obj.Set("flashVer", "FMLE/3.0 (compatible; GoRTMP)")   // クライアントバージョン
	obj.Set("tcUrl", fmt.Sprintf("rtmp://localhost/%s", c.app)) // 接続URL

	hdr, body, err := message.NewCommandMessage(0,
		"connect", c.txnID, obj,
	)
	if err != nil {
		return err
	}
	c.txnID++
	return c.writer.WriteMessage(hdr, body)
}

// sendCreateStream はcreateStreamコマンドを送信する。
func (c *Client) sendCreateStream() error {
	hdr, body, err := message.NewCommandMessage(0,
		"createStream", c.txnID, nil,
	)
	if err != nil {
		return err
	}
	c.txnID++
	return c.writer.WriteMessage(hdr, body)
}

// sendPublish はpublishコマンドを送信する。
func (c *Client) sendPublish() error {
	hdr, body, err := message.NewCommandMessage(c.streamID,
		"publish", float64(0), nil, c.streamKey, "live",
	)
	if err != nil {
		return err
	}
	return c.writer.WriteMessage(hdr, body)
}

// =============================================================================
// サーバー応答の読み取り
// =============================================================================

// readServerResponses はconnectコマンドへの応答を読み取る。
// サーバーからは以下のメッセージが返ってくる:
//   - Window Acknowledgement Size
//   - Set Peer Bandwidth
//   - Set Chunk Size
//   - _result（connect成功/失敗）
func (c *Client) readServerResponses() error {
	for {
		hdr, body, err := c.reader.ReadMessage()
		if err != nil {
			return err
		}

		switch hdr.MessageTypeID {
		case message.TypeWindowAckSize:
			log.Printf("[クライアント] Window Ack Size 受信")

		case message.TypeSetPeerBandwidth:
			log.Printf("[クライアント] Set Peer Bandwidth 受信")

		case message.TypeSetChunkSize:
			if len(body) >= 4 {
				newSize := binary.BigEndian.Uint32(body) & 0x7FFFFFFF
				c.reader.SetChunkSize(newSize)
				log.Printf("[クライアント] チャンクサイズ変更: %d", newSize)
			}

		case message.TypeCommandMessageAMF0:
			values, err := amf0.Decode(body)
			if err != nil {
				return err
			}
			if len(values) >= 1 {
				if cmdName, ok := values[0].(string); ok {
					if cmdName == "_result" {
						log.Printf("[クライアント] connect成功")
						return nil
					} else if cmdName == "_error" {
						return fmt.Errorf("connectが拒否された")
					}
				}
			}
		}
	}
}

// readCreateStreamResult はcreateStreamの応答を読み取る。
func (c *Client) readCreateStreamResult() error {
	for {
		hdr, body, err := c.reader.ReadMessage()
		if err != nil {
			return err
		}

		if hdr.MessageTypeID == message.TypeCommandMessageAMF0 {
			values, err := amf0.Decode(body)
			if err != nil {
				return err
			}
			if len(values) >= 1 {
				if cmdName, ok := values[0].(string); ok && cmdName == "_result" {
					// _result の最後の値がストリームID
					if len(values) >= 4 {
						if id, ok := values[3].(float64); ok {
							c.streamID = uint32(id)
							log.Printf("[クライアント] ストリームID取得: %d", c.streamID)
							return nil
						}
					}
				}
			}
		}

		// その他のメッセージ（Set Chunk Sizeなど）も処理
		c.handleProtocolMessage(hdr, body)
	}
}

// readUntilPublishStart はpublishのonStatus応答を待つ。
func (c *Client) readUntilPublishStart() error {
	for {
		hdr, body, err := c.reader.ReadMessage()
		if err != nil {
			return err
		}

		if hdr.MessageTypeID == message.TypeCommandMessageAMF0 {
			values, err := amf0.Decode(body)
			if err != nil {
				continue
			}
			if len(values) >= 1 {
				if cmdName, ok := values[0].(string); ok && cmdName == "onStatus" {
					log.Printf("[クライアント] onStatus受信: 配信開始")
					return nil
				}
			}
		}

		c.handleProtocolMessage(hdr, body)
	}
}

// handleProtocolMessage はプロトコル制御メッセージを処理する。
func (c *Client) handleProtocolMessage(hdr chunk.Header, body []byte) {
	switch hdr.MessageTypeID {
	case message.TypeSetChunkSize:
		if len(body) >= 4 {
			newSize := binary.BigEndian.Uint32(body) & 0x7FFFFFFF
			c.reader.SetChunkSize(newSize)
		}
	case message.TypeWindowAckSize:
		// 受信するだけ
	case message.TypeSetPeerBandwidth:
		// 受信するだけ
	}
}

// =============================================================================
// メディアデータ送信
// =============================================================================

// SendAudio は音声データを送信する。
func (c *Client) SendAudio(timestamp uint32, data []byte) error {
	hdr, body := message.NewAudioMessage(c.streamID, timestamp, data)
	return c.writer.WriteMessage(hdr, body)
}

// SendVideo は映像データを送信する。
func (c *Client) SendVideo(timestamp uint32, data []byte) error {
	hdr, body := message.NewVideoMessage(c.streamID, timestamp, data)
	return c.writer.WriteMessage(hdr, body)
}

// SendMetadata はメタデータ（@setDataFrame + onMetaData）を送信する。
func (c *Client) SendMetadata(metadata *amf0.Object) error {
	hdr, body, err := message.NewDataMessage(c.streamID, 0,
		"@setDataFrame", "onMetaData", metadata,
	)
	if err != nil {
		return err
	}
	return c.writer.WriteMessage(hdr, body)
}

// PublishFLV はFLVファイルを読み込んでRTMPで配信する。
// FLVのタイムスタンプに基づいてリアルタイムに送信する（ペース制御あり）。
//
// 【FLVからRTMP配信への変換】
// FLVタグのタイプ8（音声）→ RTMP TypeID 8 としてそのまま送信
// FLVタグのタイプ9（映像）→ RTMP TypeID 9 としてそのまま送信
// FLVタグのタイプ18（スクリプト）→ RTMP TypeID 18 としてメタデータ送信
func (c *Client) PublishFLV(r io.Reader) error {
	flvReader, err := flv.NewReader(r)
	if err != nil {
		return fmt.Errorf("FLV読み込みエラー: %w", err)
	}

	var startTime time.Time
	var firstTimestamp uint32
	started := false

	for {
		tag, err := flvReader.ReadTag()
		if err != nil {
			if err == io.EOF {
				log.Printf("[クライアント] FLV読み込み完了")
				return nil
			}
			return fmt.Errorf("FLVタグ読み込みエラー: %w", err)
		}

		// リアルタイムペース制御
		// 最初のタグの時刻を基準に、タイムスタンプの差分だけ待機する
		if !started {
			startTime = time.Now()
			firstTimestamp = tag.Timestamp
			started = true
		} else {
			// 経過すべき時間を計算
			elapsed := time.Duration(tag.Timestamp-firstTimestamp) * time.Millisecond
			// 実際の経過時間との差分だけスリープ
			wait := elapsed - time.Since(startTime)
			if wait > 0 {
				time.Sleep(wait)
			}
		}

		// FLVタグをRTMPメッセージとして送信
		switch tag.TagType {
		case flv.TagTypeAudio:
			if err := c.SendAudio(tag.Timestamp, tag.Data); err != nil {
				return err
			}
		case flv.TagTypeVideo:
			if err := c.SendVideo(tag.Timestamp, tag.Data); err != nil {
				return err
			}
		case flv.TagTypeScript:
			hdr := chunk.Header{
				ChunkStreamID:   message.CSIDCommand,
				Timestamp:       tag.Timestamp,
				MessageTypeID:   message.TypeDataMessageAMF0,
				MessageStreamID: c.streamID,
			}
			if err := c.writer.WriteMessage(hdr, tag.Data); err != nil {
				return err
			}
		}
	}
}
