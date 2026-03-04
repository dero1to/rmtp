// =============================================================================
// AMF0 (Action Message Format version 0) エンコーダ/デコーダ
// =============================================================================
//
// 【AMF0とは】
// AMF0はAdobe（旧Macromedia）が策定したバイナリシリアライズ形式。
// RTMPプロトコルのコマンドメッセージ（connect, play, publishなど）で
// パラメータをやりとりする際に使われる。
//
// 【データ型マーカー（1バイト）】
// 各値の先頭にはデータ型を示す1バイトのマーカーが付く。
// これによりデコーダ側がどの型として読めばよいか判断できる。
//
// 【対応するデータ型】
// - Number (0x00):   IEEE 754 倍精度浮動小数点数（8バイト、ビッグエンディアン）
// - Boolean (0x01):  真偽値（1バイト、0x00=false, それ以外=true）
// - String (0x02):   UTF-8文字列（2バイト長 + 文字列本体）
// - Object (0x03):   キー・バリューの集合（キーはUTF-8文字列、終端は0x000009）
// - Null (0x05):     null値（マーカーのみ）
// - ECMAArray (0x08):連想配列（4バイトの要素数 + キー・バリュー + 終端0x000009）
// - ObjectEnd (0x09):オブジェクト/配列の終端マーカー
//
// 【RTMPでの使われ方】
// 例: connect コマンドでは以下のようなAMF0値が送られる
//   1. String "connect"        （コマンド名）
//   2. Number 1.0              （トランザクションID）
//   3. Object { app: "live", ... } （コマンドオブジェクト）
//
package amf0

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// =============================================================================
// AMF0 データ型マーカー定数
// =============================================================================
// 各値の先頭1バイトでデータ型を識別する。
// RTMPの仕様上、主にNumber, Boolean, String, Object, Null, ECMAArrayが使われる。
const (
	MarkerNumber    = 0x00 // 数値型: IEEE 754 倍精度（8バイト）
	MarkerBoolean   = 0x01 // 真偽値型: 1バイト (0x00=false)
	MarkerString    = 0x02 // 文字列型: 2バイト長プレフィックス + UTF-8文字列
	MarkerObject    = 0x03 // オブジェクト型: キー・バリューの連続 + 終端マーカー
	MarkerNull      = 0x05 // null型: マーカーのみ（データ部なし）
	MarkerECMAArray = 0x08 // ECMA配列型: 4バイト要素数 + キー・バリュー + 終端
	MarkerObjectEnd = 0x09 // オブジェクト終端: 0x00 0x00 0x09 の3バイトで表現
)

// =============================================================================
// AMF0 値を表す型
// =============================================================================

// Value はAMF0の値を表す汎用型。
// Go の interface{} を使い、Number(float64), Boolean(bool), String(string),
// Object(map), Null(nil) などを格納する。
type Value = interface{}

// Object はAMF0オブジェクトを表す。
// キーの順序を保持するため、キー配列と値マップの両方を持つ。
// RTMPではconnectコマンドのプロパティなどに使われる。
type Object struct {
	Keys   []string         // キーの挿入順序を保持する配列
	Values map[string]Value // キーから値へのマッピング
}

// NewObject は空のAMF0オブジェクトを生成する。
func NewObject() *Object {
	return &Object{
		Keys:   make([]string, 0),
		Values: make(map[string]Value),
	}
}

// Set はオブジェクトにキー・バリューを追加する。
// 既存のキーの場合は値だけ更新し、新規キーの場合はキー配列にも追加する。
func (o *Object) Set(key string, value Value) {
	if _, exists := o.Values[key]; !exists {
		o.Keys = append(o.Keys, key)
	}
	o.Values[key] = value
}

// Get はオブジェクトから値を取得する。
func (o *Object) Get(key string) (Value, bool) {
	v, ok := o.Values[key]
	return v, ok
}

// =============================================================================
// エンコード関数群
// =============================================================================
// Go の値をAMF0バイナリ形式に変換する。
// 各関数はio.Writerに書き込む設計で、バッファの連結を柔軟に行える。

// Encode はGoの値をAMF0形式にエンコードしてバイト列として返す。
// 型に応じて適切なエンコード関数を呼び分ける。
func Encode(values ...Value) ([]byte, error) {
	buf := new(bytes.Buffer)
	for _, v := range values {
		if err := writeValue(buf, v); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// writeValue は単一のAMF0値をライターに書き込む。
// 値のGo型に基づいて適切なマーカーとデータを書き込む。
func writeValue(w io.Writer, v Value) error {
	switch val := v.(type) {
	case float64:
		return writeNumber(w, val)
	case int:
		// int型もfloat64に変換してNumber型として書き込む
		return writeNumber(w, float64(val))
	case bool:
		return writeBoolean(w, val)
	case string:
		return writeString(w, val)
	case *Object:
		return writeObject(w, val)
	case nil:
		return writeNull(w)
	default:
		return fmt.Errorf("amf0: サポートされていない型: %T", v)
	}
}

// writeNumber はAMF0 Number型を書き込む。
// 形式: [0x00] [8バイト IEEE 754 倍精度浮動小数点数 ビッグエンディアン]
//
// 【なぜビッグエンディアン？】
// AMF0はネットワークバイトオーダー（ビッグエンディアン）を採用している。
// これはネットワークプロトコルの慣例に従ったもの。
func writeNumber(w io.Writer, val float64) error {
	// マーカーバイト: 0x00 = Number型
	if _, err := w.Write([]byte{MarkerNumber}); err != nil {
		return err
	}
	// IEEE 754 倍精度浮動小数点数をビッグエンディアンで書き込む
	// math.Float64bits でビットパターンに変換してから書き込む
	return binary.Write(w, binary.BigEndian, math.Float64bits(val))
}

// writeBoolean はAMF0 Boolean型を書き込む。
// 形式: [0x01] [1バイト: 0x00=false, 0x01=true]
func writeBoolean(w io.Writer, val bool) error {
	if _, err := w.Write([]byte{MarkerBoolean}); err != nil {
		return err
	}
	b := byte(0x00)
	if val {
		b = 0x01
	}
	_, err := w.Write([]byte{b})
	return err
}

// writeString はAMF0 String型を書き込む。
// 形式: [0x02] [2バイト長（ビッグエンディアン）] [UTF-8文字列本体]
//
// 【文字列長の制限】
// 2バイト（uint16）で長さを表すため、最大65535バイトの文字列に対応。
// それ以上の長い文字列にはLong String (0x0C) を使う必要があるが、
// RTMPの実用上は通常の文字列型で十分。
func writeString(w io.Writer, val string) error {
	if _, err := w.Write([]byte{MarkerString}); err != nil {
		return err
	}
	return writeUTF8(w, val)
}

// writeUTF8 はAMF0の「UTF-8文字列」部分（マーカーなし）を書き込む。
// Object のキー名にも使われるため、マーカーなしの書き込みを分離している。
// 形式: [2バイト長] [文字列本体]
func writeUTF8(w io.Writer, val string) error {
	// 文字列長を2バイトのビッグエンディアンで書き込む
	strLen := uint16(len(val))
	if err := binary.Write(w, binary.BigEndian, strLen); err != nil {
		return err
	}
	// 文字列本体を書き込む
	_, err := w.Write([]byte(val))
	return err
}

// writeObject はAMF0 Object型を書き込む。
// 形式: [0x03] [キー・バリューの繰り返し...] [0x00 0x00 0x09（終端）]
//
// 【オブジェクトの構造】
// キー: UTF-8文字列（2バイト長 + 文字列本体、マーカーなし）
// バリュー: 任意のAMF0値（マーカーあり）
// 終端: 空文字列キー（0x00 0x00）+ ObjectEndマーカー（0x09）
//
// 【RTMPでの使われ方】
// connectコマンドのプロパティオブジェクトとして:
//   { app: "live", type: "nonprivate", flashVer: "FMLE/3.0", ... }
func writeObject(w io.Writer, obj *Object) error {
	if _, err := w.Write([]byte{MarkerObject}); err != nil {
		return err
	}
	// キーの挿入順序に従って書き込む（順序保持が重要）
	for _, key := range obj.Keys {
		// キー名（マーカーなしのUTF-8文字列）
		if err := writeUTF8(w, key); err != nil {
			return err
		}
		// 値（マーカーありのAMF0値）
		if err := writeValue(w, obj.Values[key]); err != nil {
			return err
		}
	}
	// オブジェクト終端マーカー: 空文字列(0x0000) + ObjectEnd(0x09)
	_, err := w.Write([]byte{0x00, 0x00, MarkerObjectEnd})
	return err
}

// writeNull はAMF0 Null型を書き込む。
// 形式: [0x05]（マーカーのみ、データ部なし）
func writeNull(w io.Writer) error {
	_, err := w.Write([]byte{MarkerNull})
	return err
}

// =============================================================================
// デコード関数群
// =============================================================================
// AMF0バイナリデータをGoの値に変換する。
// サーバーがクライアントからのコマンドを解析する際に使う。

// Decode はバイト列からAMF0値を順次デコードし、スライスとして返す。
// RTMPメッセージには複数のAMF0値が連続して格納されているため、
// EOFになるまで読み続ける。
func Decode(data []byte) ([]Value, error) {
	r := bytes.NewReader(data)
	var values []Value
	for r.Len() > 0 {
		v, err := readValue(r)
		if err != nil {
			return values, err
		}
		values = append(values, v)
	}
	return values, nil
}

// readValue はリーダーから1つのAMF0値を読み取る。
// 先頭の1バイト（マーカー）を読んで型を判定し、型に応じた読み取り関数を呼ぶ。
func readValue(r io.Reader) (Value, error) {
	// マーカーバイトを読む（1バイト）
	marker := make([]byte, 1)
	if _, err := io.ReadFull(r, marker); err != nil {
		return nil, err
	}

	switch marker[0] {
	case MarkerNumber:
		return readNumber(r)
	case MarkerBoolean:
		return readBoolean(r)
	case MarkerString:
		return readString(r)
	case MarkerObject:
		return readObject(r)
	case MarkerNull:
		// Null型はデータ部がないのでそのままnilを返す
		return nil, nil
	case MarkerECMAArray:
		return readECMAArray(r)
	default:
		return nil, fmt.Errorf("amf0: 未知のマーカー: 0x%02x", marker[0])
	}
}

// readNumber はAMF0 Number型を読み取る。
// 8バイトのビッグエンディアンIEEE 754倍精度浮動小数点数を読む。
func readNumber(r io.Reader) (float64, error) {
	var bits uint64
	if err := binary.Read(r, binary.BigEndian, &bits); err != nil {
		return 0, err
	}
	return math.Float64frombits(bits), nil
}

// readBoolean はAMF0 Boolean型を読み取る。
func readBoolean(r io.Reader) (bool, error) {
	b := make([]byte, 1)
	if _, err := io.ReadFull(r, b); err != nil {
		return false, err
	}
	return b[0] != 0x00, nil
}

// readString はAMF0 String型を読み取る（マーカーは読み取り済み）。
func readString(r io.Reader) (string, error) {
	return readUTF8(r)
}

// readUTF8 はAMF0のUTF-8文字列部分を読み取る。
// 形式: [2バイト長] [文字列本体]
func readUTF8(r io.Reader) (string, error) {
	// 文字列長を2バイトのビッグエンディアンで読む
	var strLen uint16
	if err := binary.Read(r, binary.BigEndian, &strLen); err != nil {
		return "", err
	}
	// 文字列本体を読む
	buf := make([]byte, strLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// readObject はAMF0 Object型を読み取る。
// 終端マーカー（0x00 0x00 0x09）が来るまでキー・バリューを読み続ける。
func readObject(r io.Reader) (*Object, error) {
	obj := NewObject()
	for {
		// キー名を読む（UTF-8文字列、マーカーなし）
		key, err := readUTF8(r)
		if err != nil {
			return nil, err
		}

		// 空文字列キーの場合、次のバイトがObjectEndマーカーかチェック
		if key == "" {
			marker := make([]byte, 1)
			if _, err := io.ReadFull(r, marker); err != nil {
				return nil, err
			}
			if marker[0] == MarkerObjectEnd {
				// オブジェクト終端に到達
				break
			}
			// 空文字列キーだがObjectEndでない場合（通常ありえない）
			return nil, fmt.Errorf("amf0: 空キーの後にObjectEnd(0x09)が期待されたが0x%02xが来た", marker[0])
		}

		// バリューを読む（マーカーありのAMF0値）
		val, err := readValue(r)
		if err != nil {
			return nil, err
		}
		obj.Set(key, val)
	}
	return obj, nil
}

// readECMAArray はAMF0 ECMA Array型を読み取る。
// ECMAArrayはObjectとほぼ同じ構造だが、先頭に4バイトの要素数が付く。
// ただし実際には要素数は参考値で、終端マーカーで終了を判定する。
//
// 【ECMAArrayの用途】
// RTMPでは onMetaData イベントのメタデータ（duration, width, heightなど）を
// 格納するのによく使われる。
func readECMAArray(r io.Reader) (*Object, error) {
	// 4バイトの要素数を読む（参考値として使用）
	var count uint32
	if err := binary.Read(r, binary.BigEndian, &count); err != nil {
		return nil, err
	}
	// 中身はObjectと同じ形式で読む
	return readObject(r)
}
