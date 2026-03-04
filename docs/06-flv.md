# 06. FLV コンテナフォーマット詳解

> 対応ソースコード: `pkg/flv/flv.go`

## FLV とは

**FLV (Flash Video)** は、Adobeが策定した動画コンテナフォーマット。
音声・映像・メタデータを1つのファイルにまとめて格納する。

## FLV と RTMP の密接な関係

FLVとRTMPは同じAdobe由来であり、データ構造が非常に似ている:

| RTMP | FLV | 対応 |
|---|---|---|
| 音声メッセージ (TypeID=8) | 音声タグ (TagType=8) | **同じ値** |
| 映像メッセージ (TypeID=9) | 映像タグ (TagType=9) | **同じ値** |
| データメッセージ (TypeID=18) | スクリプトタグ (TagType=18) | **同じ値** |

つまり、RTMPのメッセージタイプIDとFLVのタグタイプは同一の値を使う。
このため、RTMPで受信した音声/映像データはペイロードを一切変換せずに
そのままFLVタグとして書き出せる。逆に、FLVファイルから読み出したタグは
そのままRTMPメッセージとして送信できる。

```
RTMPメッセージ                          FLVタグ
┌─────────────┐                    ┌─────────────┐
│ TypeID: 9   │  ──── 変換不要 ──→ │ TagType: 9  │
│ TS: 1000ms  │                    │ TS: 1000ms  │
│ Data: [...]  │                    │ Data: [...]  │
└─────────────┘                    └─────────────┘
```

## FLV ファイルの全体構造

```
+==================+
|   FLV Header     |  9バイト（ファイル先頭に1回だけ）
+==================+
| PreviousTagSize0 |  4バイト（常に0）
+==================+
|   FLV Tag 1      |  11バイトヘッダ + データ
+==================+
| PreviousTagSize1 |  4バイト
+==================+
|   FLV Tag 2      |
+==================+
| PreviousTagSize2 |  4バイト
+==================+
|   ...            |
```

## FLV Header（9バイト）

ファイルの先頭に1回だけ出現する。

```
+-------+-------+-------+---------+-------+------------------+
| 'F'   | 'L'   | 'V'   | Version | Flags | Header Size      |
| 0x46  | 0x4C  | 0x56  | 0x01    |       | (4 bytes, BE)    |
+-------+-------+-------+---------+-------+------------------+
 1byte   1byte   1byte    1byte    1byte       4bytes
```

| フィールド | サイズ | 値 | 説明 |
|---|---|---|---|
| Signature | 3バイト | "FLV" (0x46 0x4C 0x56) | ファイルフォーマット識別子 |
| Version | 1バイト | 0x01 | FLVバージョン（常に1） |
| Flags | 1バイト | ビットフラグ | トラック情報 |
| Header Size | 4バイト | 0x00000009 | ヘッダのサイズ（常に9） |

### Flags の詳細

```
ビット: 7 6 5 4 3 2 1 0
       +-+-+-+-+-+-+-+-+
       |0|0|0|0|0|A|0|V|
       +-+-+-+-+-+-+-+-+
                   │   │
                   │   └── bit 0: 映像トラックあり (1=あり)
                   └────── bit 2: 音声トラックあり (1=あり)
```

| Flags値 | 意味 |
|---|---|
| 0x05 | 音声+映像 (0b00000101) |
| 0x04 | 音声のみ (0b00000100) |
| 0x01 | 映像のみ (0b00000001) |

**対応するソースコード:**
```go
flags := byte(0)
if fw.hasAudio {
    flags |= 0x04 // bit2: 音声あり
}
if fw.hasVideo {
    flags |= 0x01 // bit0: 映像あり
}
header[4] = flags
```

## FLV Tag（11バイトヘッダ + データ）

各タグは音声・映像・スクリプトデータのいずれかを格納する。

```
+----------+------------------+------------------+----------+-----------+
| TagType  | DataSize         | Timestamp        | TS Ext   | StreamID  |
| (1 byte) | (3 bytes, BE)    | (3 bytes, BE)    | (1 byte) | (3 bytes) |
+----------+------------------+------------------+----------+-----------+
|                          Tag Data (DataSize bytes)                     |
+------------------------------------------------------------------------+
```

| フィールド | サイズ | 説明 |
|---|---|---|
| TagType | 1バイト | タグの種類: 8=音声, 9=映像, 18=スクリプト |
| DataSize | 3バイト (BE) | タグデータのバイト数 |
| Timestamp | 3バイト (BE) | タイムスタンプの下位24ビット（ミリ秒） |
| TimestampExtended | 1バイト | タイムスタンプの上位8ビット |
| StreamID | 3バイト | 常に0（FLVでは使わない） |
| Tag Data | 可変長 | 実際の音声/映像/スクリプトデータ |

### タイムスタンプの組み立て

FLVのタイムスタンプは少し変則的な配置になっている:

```
バイト4: Timestamp[23:16]  (上位)
バイト5: Timestamp[15:8]   (中位)
バイト6: Timestamp[7:0]    (下位)
バイト7: Timestamp[31:24]  (拡張 = 最上位)
```

つまり、32ビットのタイムスタンプが「下位24ビット → 上位8ビット」の順で格納される。

```go
// エンコード（書き込み）
tagHeader[4] = byte(timestamp >> 16) // 下位24ビットの上位部分
tagHeader[5] = byte(timestamp >> 8)
tagHeader[6] = byte(timestamp)
tagHeader[7] = byte(timestamp >> 24) // 拡張（上位8ビット）

// デコード（読み取り）
tag.Timestamp = uint32(tagHeader[4])<<16 | uint32(tagHeader[5])<<8 | uint32(tagHeader[6])
tag.Timestamp |= uint32(tagHeader[7]) << 24 // 拡張タイムスタンプを合成
```

## PreviousTagSize

各タグの後に4バイトのPreviousTagSizeが付く。
直前のタグの総サイズ（11バイトヘッダ + DataSize）を格納する。

```
PreviousTagSize = 11 + DataSize
```

これにより、FLVファイルを **逆方向にシーク** できる。
プレーヤーがファイル末尾から遡ってキーフレームを探す際に使われる。

## FLV の実用的な用途

### 1. RTMPサーバーでの録画

```
RTMPサーバー
    │
    │ 受信: TypeID=8 (音声), TS=1000, Data=[...]
    │   └→ FLVWriter.WriteTag(8, 1000, data)
    │
    │ 受信: TypeID=9 (映像), TS=1033, Data=[...]
    │   └→ FLVWriter.WriteTag(9, 1033, data)
    │
    ▼
  output.flv
```

**対応するソースコード:**
```go
// server/server.go - handleMediaMessage 内
if c.flvWriter != nil {
    tagType := byte(flv.TagTypeAudio)
    if hdr.MessageTypeID == message.TypeVideoMessage {
        tagType = flv.TagTypeVideo
    }
    c.flvWriter.WriteTag(tagType, hdr.Timestamp, body)
}
```

### 2. FLVファイルからのRTMP配信

```
  input.flv
    │
    │ 読み取り: TagType=9, TS=0, Data=[...]
    │   └→ client.SendVideo(0, data)
    │
    │ 読み取り: TagType=8, TS=23, Data=[...]
    │   └→ client.SendAudio(23, data)
    │
    │ (タイムスタンプに基づいてペース制御)
    │
    ▼
  RTMPサーバーに送信
```

**対応するソースコード:**
```go
// client/client.go - PublishFLV 内
switch tag.TagType {
case flv.TagTypeAudio:
    c.SendAudio(tag.Timestamp, tag.Data)
case flv.TagTypeVideo:
    c.SendVideo(tag.Timestamp, tag.Data)
case flv.TagTypeScript:
    // メタデータとしてそのまま送信
}
```

## 典型的なFLVファイルのタグ順序

```
Tag 1:  Script  (onMetaData)       TS=0     ← メタ情報
Tag 2:  Video   (AVC Seq Header)   TS=0     ← 映像デコーダ設定
Tag 3:  Audio   (AAC Seq Header)   TS=0     ← 音声デコーダ設定
Tag 4:  Video   (Keyframe)         TS=0     ← 最初のキーフレーム
Tag 5:  Audio   (AAC Raw)          TS=23    ← 音声データ
Tag 6:  Audio   (AAC Raw)          TS=46    ← 音声データ
Tag 7:  Video   (P-frame)          TS=33    ← 差分フレーム
Tag 8:  Audio   (AAC Raw)          TS=69    ← 音声データ
...
```

最初の3つのタグ（メタデータ、映像ヘッダ、音声ヘッダ）はタイムスタンプ0で、
配信の前提条件を設定する。その後に実際のメディアデータが続く。

## テストの説明

`pkg/flv/flv_test.go` では:

1. FLVヘッダとタグの書き込み → 読み取りの往復テスト
2. タグタイプ（音声/映像）の正しい保持
3. タイムスタンプの正しいエンコード/デコード
4. 複数タグの連続読み書き
