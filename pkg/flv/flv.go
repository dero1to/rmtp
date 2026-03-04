// =============================================================================
// FLV (Flash Video) ファイル書き込み/読み込み
// =============================================================================
//
// 【FLVとは】
// Adobeが策定した動画コンテナフォーマット。
// RTMPで受信した音声・映像データをファイルに保存する際の標準形式。
// RTMPの音声/映像メッセージはFLVタグとほぼ同じ構造なので、
// RTMPから受け取ったデータをそのままFLVファイルに書き出せる。
//
// 【FLVファイルの構造】
//
//   +------------------+
//   | FLV Header       |  9バイト（固定）
//   +------------------+
//   | PreviousTagSize0 |  4バイト（常に0）
//   +------------------+
//   | FLV Tag 1        |  11バイトヘッダ + データ
//   +------------------+
//   | PreviousTagSize1 |  4バイト
//   +------------------+
//   | FLV Tag 2        |
//   +------------------+
//   | PreviousTagSize2 |
//   +------------------+
//   | ...              |
//
// 【FLV Header（9バイト）】
//   - シグネチャ "FLV" (3バイト)
//   - バージョン (1バイト): 通常は1
//   - フラグ (1バイト): bit0=音声あり, bit2=映像あり
//   - ヘッダサイズ (4バイト): 通常は9
//
// 【FLV Tag（11バイト + データ）】
//   - タグタイプ (1バイト): 8=音声, 9=映像, 18=スクリプトデータ
//   - データサイズ (3バイト)
//   - タイムスタンプ (3バイト): 下位24ビット
//   - タイムスタンプ拡張 (1バイト): 上位8ビット
//   - ストリームID (3バイト): 常に0
//   - データ (可変長)
//
// 【RTMPとFLVの関係】
// RTMPのTypeID 8（音声）→ FLVのタグタイプ8
// RTMPのTypeID 9（映像）→ FLVのタグタイプ9
// RTMPのTypeID 18（データ）→ FLVのタグタイプ18
// つまり、RTMPのメッセージタイプIDがそのままFLVのタグタイプになる。
//
package flv

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// FLVタグタイプ（RTMPのメッセージタイプIDと同じ値）
const (
	TagTypeAudio  = 8  // 音声タグ
	TagTypeVideo  = 9  // 映像タグ
	TagTypeScript = 18 // スクリプトデータタグ（メタデータ等）
)

// =============================================================================
// FLV Writer（FLVファイル書き込み）
// =============================================================================

// Writer はFLVファイルにタグを書き込むための構造体。
// RTMPサーバーが受信した音声/映像データを録画する際に使う。
type Writer struct {
	w        io.Writer
	hasAudio bool // 音声トラックが含まれるか
	hasVideo bool // 映像トラックが含まれるか
}

// NewWriter は新しいFLVライターを作成し、FLVヘッダを書き込む。
// hasAudio: 音声トラックを含むか
// hasVideo: 映像トラックを含むか
func NewWriter(w io.Writer, hasAudio, hasVideo bool) (*Writer, error) {
	fw := &Writer{
		w:        w,
		hasAudio: hasAudio,
		hasVideo: hasVideo,
	}

	// FLVヘッダを書き込む（9バイト）
	if err := fw.writeHeader(); err != nil {
		return nil, err
	}

	// PreviousTagSize0 を書き込む（常に0）
	if err := binary.Write(w, binary.BigEndian, uint32(0)); err != nil {
		return nil, err
	}

	return fw, nil
}

// writeHeader はFLVファイルヘッダを書き込む。
func (fw *Writer) writeHeader() error {
	header := make([]byte, 9)
	// シグネチャ "FLV"
	header[0] = 'F'
	header[1] = 'L'
	header[2] = 'V'
	// バージョン: 1
	header[3] = 0x01
	// フラグ: bit0=音声, bit2=映像
	flags := byte(0)
	if fw.hasAudio {
		flags |= 0x04 // bit2: 音声あり
	}
	if fw.hasVideo {
		flags |= 0x01 // bit0: 映像あり
	}
	header[4] = flags
	// ヘッダサイズ: 9（ビッグエンディアン）
	binary.BigEndian.PutUint32(header[5:9], 9)

	_, err := fw.w.Write(header)
	return err
}

// WriteTag はFLVタグを書き込む。
// RTMPから受信したメッセージをそのままFLVタグとして書き出せる。
//
// パラメータ:
//   - tagType: タグタイプ（8=音声, 9=映像, 18=スクリプト）
//   - timestamp: タイムスタンプ（ミリ秒）
//   - data: タグデータ（音声/映像のペイロード）
func (fw *Writer) WriteTag(tagType byte, timestamp uint32, data []byte) error {
	dataSize := uint32(len(data))

	// FLVタグヘッダ（11バイト）
	tagHeader := make([]byte, 11)
	// タグタイプ（1バイト）
	tagHeader[0] = tagType
	// データサイズ（3バイト、ビッグエンディアン）
	tagHeader[1] = byte(dataSize >> 16)
	tagHeader[2] = byte(dataSize >> 8)
	tagHeader[3] = byte(dataSize)
	// タイムスタンプ下位24ビット（3バイト）
	tagHeader[4] = byte(timestamp >> 16)
	tagHeader[5] = byte(timestamp >> 8)
	tagHeader[6] = byte(timestamp)
	// タイムスタンプ拡張（上位8ビット）
	tagHeader[7] = byte(timestamp >> 24)
	// ストリームID（3バイト、常に0）
	tagHeader[8] = 0
	tagHeader[9] = 0
	tagHeader[10] = 0

	// タグヘッダを書き込む
	if _, err := fw.w.Write(tagHeader); err != nil {
		return fmt.Errorf("FLVタグヘッダの書き込みに失敗: %w", err)
	}

	// タグデータを書き込む
	if _, err := fw.w.Write(data); err != nil {
		return fmt.Errorf("FLVタグデータの書き込みに失敗: %w", err)
	}

	// PreviousTagSize を書き込む（タグヘッダ11バイト + データサイズ）
	prevTagSize := uint32(11) + dataSize
	if err := binary.Write(fw.w, binary.BigEndian, prevTagSize); err != nil {
		return fmt.Errorf("PreviousTagSizeの書き込みに失敗: %w", err)
	}

	return nil
}

// =============================================================================
// FLV Reader（FLVファイル読み込み）
// =============================================================================

// Tag はFLVファイルから読み取った1つのタグを表す。
type Tag struct {
	TagType   byte   // タグタイプ: 8=音声, 9=映像, 18=スクリプト
	Timestamp uint32 // タイムスタンプ（ミリ秒）
	Data      []byte // タグデータ
}

// Reader はFLVファイルからタグを順次読み取るための構造体。
// クライアントがFLVファイルを読んでRTMPで送信する際に使う。
type Reader struct {
	r io.Reader
}

// NewReader はFLVファイルからReaderを作成する。
// FLVヘッダとPreviousTagSize0を読み飛ばす。
func NewReader(r io.Reader) (*Reader, error) {
	// FLVヘッダ（9バイト）を読む
	header := make([]byte, 9)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("FLVヘッダの読み取りに失敗: %w", err)
	}

	// シグネチャの確認
	if header[0] != 'F' || header[1] != 'L' || header[2] != 'V' {
		return nil, fmt.Errorf("FLVシグネチャが不正: %v", header[0:3])
	}

	// PreviousTagSize0（4バイト）を読み飛ばす
	skip := make([]byte, 4)
	if _, err := io.ReadFull(r, skip); err != nil {
		return nil, fmt.Errorf("PreviousTagSize0の読み取りに失敗: %w", err)
	}

	return &Reader{r: r}, nil
}

// ReadTag はFLVファイルから次のタグを読み取る。
// ファイル末尾に到達した場合はio.EOFを返す。
func (fr *Reader) ReadTag() (*Tag, error) {
	// タグヘッダ（11バイト）を読む
	tagHeader := make([]byte, 11)
	if _, err := io.ReadFull(fr.r, tagHeader); err != nil {
		return nil, err // io.EOF の場合はそのまま返す
	}

	tag := &Tag{}
	// タグタイプ
	tag.TagType = tagHeader[0]
	// データサイズ（3バイト、ビッグエンディアン）
	dataSize := uint32(tagHeader[1])<<16 | uint32(tagHeader[2])<<8 | uint32(tagHeader[3])
	// タイムスタンプ（下位24ビット + 拡張8ビット）
	tag.Timestamp = uint32(tagHeader[4])<<16 | uint32(tagHeader[5])<<8 | uint32(tagHeader[6])
	tag.Timestamp |= uint32(tagHeader[7]) << 24 // 拡張タイムスタンプ

	// タグデータを読む
	tag.Data = make([]byte, dataSize)
	if _, err := io.ReadFull(fr.r, tag.Data); err != nil {
		return nil, fmt.Errorf("FLVタグデータの読み取りに失敗: %w", err)
	}

	// PreviousTagSize（4バイト）を読み飛ばす
	skip := make([]byte, 4)
	if _, err := io.ReadFull(fr.r, skip); err != nil {
		// ファイル末尾の場合、最後のタグは正常に返す
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return tag, nil
		}
		return nil, err
	}

	return tag, nil
}

// OpenFile はFLVファイルを開いてReaderを作成するヘルパー関数。
func OpenFile(path string) (*Reader, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	reader, err := NewReader(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return reader, f, nil
}
