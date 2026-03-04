// =============================================================================
// RTMP チャンクストリーム層
// =============================================================================
//
// 【チャンクストリームとは】
// RTMPメッセージをTCPで送受信するための多重化・分割レイヤー。
// 大きなメッセージを小さな「チャンク」に分割し、複数のストリームを
// 1本のTCP接続上で効率的に多重化できるようにする。
//
// 【なぜチャンクに分割するのか】
// 1. 大きな映像フレーム（数十KB〜数MB）が小さな音声パケットや
//    制御メッセージをブロックしないようにするため（HOL blocking防止）
// 2. 帯域幅を効率的に共有するため
// 3. ヘッダ圧縮により通信量を削減するため
//
// 【チャンクの構造】
//
//   +-------------------+-------------------+-------------------+
//   | Basic Header      | Message Header    | Chunk Data        |
//   | (1〜3バイト)      | (0/3/7/11バイト)  | (可変長)           |
//   +-------------------+-------------------+-------------------+
//
// 【Basic Header（基本ヘッダ）】
// チャンクストリームIDとフォーマットタイプを含む。
//
//   ビット配置: [fmt(2bit)][csid(6bit)]
//
//   fmt (2ビット): メッセージヘッダのフォーマットタイプ（0〜3）
//     - Type 0: 完全ヘッダ（11バイト）— ストリームの最初のチャンクに使用
//     - Type 1: 短縮ヘッダ（7バイト）— 同じストリームIDの後続メッセージ
//     - Type 2: 最小ヘッダ（3バイト）— タイムスタンプ差分のみ
//     - Type 3: ヘッダなし（0バイト）— 前のチャンクと全て同じ
//
//   csid (6ビット): チャンクストリームID
//     - 2: プロトコル制御メッセージ用（固定）
//     - 3: コマンドメッセージ用
//     - 4以降: 音声・映像データ用
//     ※ 0, 1は特殊用途（拡張ヘッダ）
//
// 【Message Header（メッセージヘッダ）】
// フォーマットタイプ(fmt)によりサイズが変わる:
//
//   Type 0 (11バイト): 完全なメッセージ情報
//     - タイムスタンプ (3バイト)
//     - メッセージ長 (3バイト)
//     - メッセージタイプID (1バイト)
//     - メッセージストリームID (4バイト, リトルエンディアン)
//
//   Type 1 (7バイト): ストリームIDを省略
//     - タイムスタンプ差分 (3バイト)
//     - メッセージ長 (3バイト)
//     - メッセージタイプID (1バイト)
//
//   Type 2 (3バイト): タイムスタンプ差分のみ
//     - タイムスタンプ差分 (3バイト)
//
//   Type 3 (0バイト): ヘッダなし（継続チャンク）
//
// 【Extended Timestamp（拡張タイムスタンプ）】
// タイムスタンプが0xFFFFFF (16777215) 以上の場合、
// 3バイトフィールドには0xFFFFFFを入れ、直後に4バイトの拡張タイムスタンプを追加する。
//
package chunk

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// デフォルトのチャンクサイズ（128バイト）
// RTMPの仕様上、初期値は128。Set Chunk Sizeメッセージで変更可能。
// 映像配信では4096バイト程度に上げるのが一般的（分割回数を減らすため）。
const DefaultChunkSize = 128

// =============================================================================
// チャンクヘッダ情報
// =============================================================================

// Header はRTMPチャンクのヘッダ情報を保持する構造体。
// チャンクの読み書き時に、メッセージの属性を伝達するために使う。
type Header struct {
	// Fmt はフォーマットタイプ（0〜3）
	// チャンク送信時のヘッダ圧縮レベルを決定する
	Fmt byte

	// ChunkStreamID はチャンクストリームの識別子（2〜65599）
	// 同じcsidのチャンクは論理的に同じストリームに属する
	ChunkStreamID uint32

	// Timestamp はメッセージのタイムスタンプ（ミリ秒）
	// 音声・映像の再生タイミングの基準となる重要な値
	Timestamp uint32

	// MessageLength はメッセージ全体のバイト数
	// チャンクに分割される前の元のメッセージのサイズ
	MessageLength uint32

	// MessageTypeID はメッセージの種類を示すID
	// 例: 1=Set Chunk Size, 8=Audio, 9=Video, 20=AMFコマンド
	MessageTypeID byte

	// MessageStreamID はメッセージストリームの識別子
	// RTMPのアプリケーション層でストリームを区別するために使う
	// ※ リトルエンディアンで格納される（RTMP仕様の例外的な部分）
	MessageStreamID uint32
}

// =============================================================================
// チャンクリーダー
// =============================================================================

// Reader はTCPストリームからRTMPチャンクを読み取る。
// 複数のチャンクストリームの状態を管理し、チャンクをメッセージに再構築する。
type Reader struct {
	r         *bufio.Reader
	chunkSize uint32                // 現在のチャンクサイズ（受信側）
	prevState map[uint32]*chunkState // csid → 前回のチャンク状態
	mu        sync.Mutex
}

// chunkState は各チャンクストリームの前回のヘッダ情報を保持する。
// Type 1, 2, 3 のヘッダ圧縮をデコードする際に前回の値を参照するため必要。
type chunkState struct {
	header    Header // 前回のヘッダ情報
	remaining uint32 // メッセージの未受信バイト数
	body      []byte // 受信途中のメッセージボディ
}

// NewReader は新しいチャンクリーダーを作成する。
func NewReader(r io.Reader) *Reader {
	return &Reader{
		r:         bufio.NewReaderSize(r, 4096),
		chunkSize: DefaultChunkSize,
		prevState: make(map[uint32]*chunkState),
	}
}

// SetChunkSize は受信側のチャンクサイズを変更する。
// Set Chunk Size プロトコル制御メッセージを受信した際に呼ぶ。
func (cr *Reader) SetChunkSize(size uint32) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.chunkSize = size
}

// ReadMessage はチャンクを読み取り、完全なメッセージとして返す。
// 分割されたチャンクを結合し、メッセージが完成するまで読み続ける。
//
// 戻り値:
//   - Header: メッセージのヘッダ情報
//   - []byte: メッセージボディ（全チャンクを結合したもの）
//   - error: エラー
func (cr *Reader) ReadMessage() (Header, []byte, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	for {
		// チャンクヘッダを読む
		hdr, err := cr.readChunkHeader()
		if err != nil {
			return Header{}, nil, err
		}

		// このチャンクストリームの状態を取得（なければ新規作成）
		state, ok := cr.prevState[hdr.ChunkStreamID]
		if !ok {
			state = &chunkState{}
			cr.prevState[hdr.ChunkStreamID] = state
		}

		// フォーマットタイプに応じてヘッダ情報を補完する
		switch hdr.Fmt {
		case 0:
			// Type 0: 全フィールドが含まれる（完全ヘッダ）
			state.header = hdr
			state.remaining = hdr.MessageLength
			state.body = make([]byte, 0, hdr.MessageLength)
		case 1:
			// Type 1: MessageStreamIDは前回の値を引き継ぐ
			hdr.MessageStreamID = state.header.MessageStreamID
			state.header = hdr
			state.remaining = hdr.MessageLength
			state.body = make([]byte, 0, hdr.MessageLength)
		case 2:
			// Type 2: MessageLength, MessageTypeID, MessageStreamIDは前回の値
			hdr.MessageLength = state.header.MessageLength
			hdr.MessageTypeID = state.header.MessageTypeID
			hdr.MessageStreamID = state.header.MessageStreamID
			state.header = hdr
			state.remaining = hdr.MessageLength
			state.body = make([]byte, 0, hdr.MessageLength)
		case 3:
			// Type 3: 全て前回の値（継続チャンク）
			hdr = state.header
			// remainingとbodyはそのまま継続
		}

		// チャンクデータを読む（チャンクサイズまたは残りのメッセージ長のうち小さい方）
		readSize := cr.chunkSize
		if state.remaining < readSize {
			readSize = state.remaining
		}

		chunkData := make([]byte, readSize)
		if _, err := io.ReadFull(cr.r, chunkData); err != nil {
			return Header{}, nil, fmt.Errorf("チャンクデータの読み取りに失敗: %w", err)
		}

		state.body = append(state.body, chunkData...)
		state.remaining -= readSize

		// メッセージが完成したら返す
		if state.remaining == 0 {
			body := state.body
			state.body = nil
			return hdr, body, nil
		}
		// まだメッセージが完成していない場合、次のチャンクを読み続ける
	}
}

// readChunkHeader はBasic HeaderとMessage Headerを読み取る。
func (cr *Reader) readChunkHeader() (Header, error) {
	var hdr Header

	// -----------------------------------------------------------------------
	// Basic Header の読み取り（1〜3バイト）
	// -----------------------------------------------------------------------
	// 先頭1バイトの上位2ビット = fmt, 下位6ビット = csid
	firstByte, err := cr.r.ReadByte()
	if err != nil {
		return hdr, fmt.Errorf("Basic Headerの読み取りに失敗: %w", err)
	}

	hdr.Fmt = (firstByte >> 6) & 0x03   // 上位2ビット
	csid := uint32(firstByte & 0x3F)     // 下位6ビット

	switch csid {
	case 0:
		// csid=0: 2バイト形式（csid = 64 + 次の1バイト）
		// csid 64〜319 に対応
		b, err := cr.r.ReadByte()
		if err != nil {
			return hdr, err
		}
		hdr.ChunkStreamID = uint32(b) + 64
	case 1:
		// csid=1: 3バイト形式（csid = 64 + 次の2バイト）
		// csid 64〜65599 に対応
		buf := make([]byte, 2)
		if _, err := io.ReadFull(cr.r, buf); err != nil {
			return hdr, err
		}
		hdr.ChunkStreamID = uint32(buf[0]) + uint32(buf[1])*256 + 64
	default:
		// csid=2〜63: 1バイト形式（そのまま使用）
		hdr.ChunkStreamID = csid
	}

	// -----------------------------------------------------------------------
	// Message Header の読み取り（fmtに応じてサイズが変わる）
	// -----------------------------------------------------------------------
	switch hdr.Fmt {
	case 0:
		// Type 0: 11バイト（完全ヘッダ）
		buf := make([]byte, 11)
		if _, err := io.ReadFull(cr.r, buf); err != nil {
			return hdr, fmt.Errorf("Type 0ヘッダの読み取りに失敗: %w", err)
		}
		// タイムスタンプ（3バイト、ビッグエンディアン）
		hdr.Timestamp = uint32(buf[0])<<16 | uint32(buf[1])<<8 | uint32(buf[2])
		// メッセージ長（3バイト、ビッグエンディアン）
		hdr.MessageLength = uint32(buf[3])<<16 | uint32(buf[4])<<8 | uint32(buf[5])
		// メッセージタイプID（1バイト）
		hdr.MessageTypeID = buf[6]
		// メッセージストリームID（4バイト、リトルエンディアン ← RTMP仕様の例外）
		hdr.MessageStreamID = binary.LittleEndian.Uint32(buf[7:11])

		// 拡張タイムスタンプのチェック
		if hdr.Timestamp == 0xFFFFFF {
			hdr.Timestamp, err = cr.readExtendedTimestamp()
			if err != nil {
				return hdr, err
			}
		}

	case 1:
		// Type 1: 7バイト（MessageStreamIDを省略）
		buf := make([]byte, 7)
		if _, err := io.ReadFull(cr.r, buf); err != nil {
			return hdr, fmt.Errorf("Type 1ヘッダの読み取りに失敗: %w", err)
		}
		hdr.Timestamp = uint32(buf[0])<<16 | uint32(buf[1])<<8 | uint32(buf[2])
		hdr.MessageLength = uint32(buf[3])<<16 | uint32(buf[4])<<8 | uint32(buf[5])
		hdr.MessageTypeID = buf[6]

		if hdr.Timestamp == 0xFFFFFF {
			hdr.Timestamp, err = cr.readExtendedTimestamp()
			if err != nil {
				return hdr, err
			}
		}

	case 2:
		// Type 2: 3バイト（タイムスタンプ差分のみ）
		buf := make([]byte, 3)
		if _, err := io.ReadFull(cr.r, buf); err != nil {
			return hdr, fmt.Errorf("Type 2ヘッダの読み取りに失敗: %w", err)
		}
		hdr.Timestamp = uint32(buf[0])<<16 | uint32(buf[1])<<8 | uint32(buf[2])

		if hdr.Timestamp == 0xFFFFFF {
			hdr.Timestamp, err = cr.readExtendedTimestamp()
			if err != nil {
				return hdr, err
			}
		}

	case 3:
		// Type 3: 0バイト（ヘッダなし、継続チャンク）
		// 前のチャンクと同一の属性を使う
	}

	return hdr, nil
}

// readExtendedTimestamp は拡張タイムスタンプ（4バイト）を読み取る。
// タイムスタンプ値が0xFFFFFF以上の場合に使われる。
func (cr *Reader) readExtendedTimestamp() (uint32, error) {
	var ts uint32
	if err := binary.Read(cr.r, binary.BigEndian, &ts); err != nil {
		return 0, fmt.Errorf("拡張タイムスタンプの読み取りに失敗: %w", err)
	}
	return ts, nil
}

// =============================================================================
// チャンクライター
// =============================================================================

// Writer はRTMPメッセージをチャンクに分割してTCPストリームに書き込む。
type Writer struct {
	w         *bufio.Writer
	chunkSize uint32                // 現在のチャンクサイズ（送信側）
	prevState map[uint32]*Header    // csid → 前回送信したヘッダ
	mu        sync.Mutex
}

// NewWriter は新しいチャンクライターを作成する。
func NewWriter(w io.Writer) *Writer {
	return &Writer{
		w:         bufio.NewWriterSize(w, 4096),
		chunkSize: DefaultChunkSize,
		prevState: make(map[uint32]*Header),
	}
}

// SetChunkSize は送信側のチャンクサイズを変更する。
func (cw *Writer) SetChunkSize(size uint32) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.chunkSize = size
}

// WriteMessage はメッセージをチャンクに分割して書き込む。
//
// 分割の流れ:
//   1. 最初のチャンクはType 0ヘッダ（完全ヘッダ）で送る
//   2. 継続チャンクはType 3ヘッダ（ヘッダなし）で送る
//   3. 各チャンクのデータ部はchunkSizeバイト以下
//
// 【ヘッダ圧縮について】
// 本実装では簡略化のため常にType 0を使用している。
// より効率的な実装では、前回のヘッダと比較してType 1〜3を使い分ける。
func (cw *Writer) WriteMessage(hdr Header, body []byte) error {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	hdr.MessageLength = uint32(len(body))
	offset := uint32(0)
	first := true

	for offset < uint32(len(body)) {
		// 今回のチャンクで書き込むサイズを決定
		writeSize := cw.chunkSize
		remaining := uint32(len(body)) - offset
		if remaining < writeSize {
			writeSize = remaining
		}

		if first {
			// 最初のチャンクはType 0（完全ヘッダ）
			if err := cw.writeChunkHeader(hdr, 0); err != nil {
				return err
			}
			first = false
		} else {
			// 継続チャンクはType 3（ヘッダなし）
			if err := cw.writeChunkHeader(hdr, 3); err != nil {
				return err
			}
		}

		// チャンクデータを書き込む
		if _, err := cw.w.Write(body[offset : offset+writeSize]); err != nil {
			return fmt.Errorf("チャンクデータの書き込みに失敗: %w", err)
		}
		offset += writeSize
	}

	// バッファをフラッシュ（TCPに送出）
	if err := cw.w.Flush(); err != nil {
		return fmt.Errorf("フラッシュに失敗: %w", err)
	}

	// 今回のヘッダを保存（次回のヘッダ圧縮のため）
	saved := hdr
	cw.prevState[hdr.ChunkStreamID] = &saved

	return nil
}

// writeChunkHeader はBasic HeaderとMessage Headerを書き込む。
func (cw *Writer) writeChunkHeader(hdr Header, fmtType byte) error {
	// -----------------------------------------------------------------------
	// Basic Header の書き込み
	// -----------------------------------------------------------------------
	csid := hdr.ChunkStreamID

	if csid >= 2 && csid <= 63 {
		// 1バイト形式: [fmt(2bit)][csid(6bit)]
		basicHeader := (fmtType << 6) | byte(csid)
		if err := cw.w.WriteByte(basicHeader); err != nil {
			return err
		}
	} else if csid >= 64 && csid <= 319 {
		// 2バイト形式: [fmt(2bit)][000000] [csid-64]
		basicHeader := fmtType << 6 // csid部分は0
		if err := cw.w.WriteByte(basicHeader); err != nil {
			return err
		}
		if err := cw.w.WriteByte(byte(csid - 64)); err != nil {
			return err
		}
	} else {
		// 3バイト形式: [fmt(2bit)][000001] [csid-64の下位8ビット] [csid-64の上位8ビット]
		basicHeader := (fmtType << 6) | 0x01
		if err := cw.w.WriteByte(basicHeader); err != nil {
			return err
		}
		val := csid - 64
		if err := cw.w.WriteByte(byte(val & 0xFF)); err != nil {
			return err
		}
		if err := cw.w.WriteByte(byte(val >> 8)); err != nil {
			return err
		}
	}

	// -----------------------------------------------------------------------
	// Message Header の書き込み
	// -----------------------------------------------------------------------
	timestamp := hdr.Timestamp
	useExtendedTimestamp := timestamp >= 0xFFFFFF

	switch fmtType {
	case 0:
		// Type 0: 11バイト（完全ヘッダ）
		buf := make([]byte, 11)
		ts := timestamp
		if useExtendedTimestamp {
			ts = 0xFFFFFF
		}
		// タイムスタンプ（3バイト、ビッグエンディアン）
		buf[0] = byte(ts >> 16)
		buf[1] = byte(ts >> 8)
		buf[2] = byte(ts)
		// メッセージ長（3バイト、ビッグエンディアン）
		buf[3] = byte(hdr.MessageLength >> 16)
		buf[4] = byte(hdr.MessageLength >> 8)
		buf[5] = byte(hdr.MessageLength)
		// メッセージタイプID（1バイト）
		buf[6] = hdr.MessageTypeID
		// メッセージストリームID（4バイト、リトルエンディアン）
		binary.LittleEndian.PutUint32(buf[7:11], hdr.MessageStreamID)

		if _, err := cw.w.Write(buf); err != nil {
			return err
		}

		// 拡張タイムスタンプ（必要な場合のみ）
		if useExtendedTimestamp {
			if err := binary.Write(cw.w, binary.BigEndian, timestamp); err != nil {
				return err
			}
		}

	case 3:
		// Type 3: 0バイト（ヘッダなし）
		// Basic Header のみで、Message Header は書かない
	}

	return nil
}

// Flush はバッファに残っているデータを強制的に書き出す。
func (cw *Writer) Flush() error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	return cw.w.Flush()
}
