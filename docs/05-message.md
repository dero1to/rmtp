# 05. メッセージ層詳解

> 対応ソースコード: `pkg/message/message.go`

## メッセージ層とは

RTMPのアプリケーション層に近い部分で、チャンクストリーム層の上に位置する。
メッセージタイプID（TypeID）によって、プロトコル制御・コマンド・音声・映像などの
具体的なメッセージを区別する。

## メッセージタイプ一覧

チャンクヘッダの `MessageTypeID`（1バイト）で種別が決まる。

### プロトコル制御メッセージ（TypeID 1〜6）

チャンクストリーム層の動作を制御する低レベルなメッセージ。
**必ず csid=2, messageStreamID=0** で送る決まりがある。

| TypeID | 名前 | データサイズ | 説明 |
|---|---|---|---|
| 1 | Set Chunk Size | 4バイト | チャンクの最大サイズを変更 |
| 2 | Abort Message | 4バイト | 指定csidの未完成メッセージを破棄 |
| 3 | Acknowledgement | 4バイト | 受信バイト数の確認応答 |
| 4 | User Control Message | 可変長 | ストリームイベント通知 |
| 5 | Window Acknowledgement Size | 4バイト | Ack送信間隔を設定 |
| 6 | Set Peer Bandwidth | 5バイト | 相手の送信帯域幅上限を設定 |

### メディア・コマンドメッセージ（TypeID 8〜20）

| TypeID | 名前 | 説明 |
|---|---|---|
| 8 | Audio Message | 音声データ |
| 9 | Video Message | 映像データ |
| 18 | Data Message (AMF0) | メタデータ（onMetaDataなど） |
| 20 | Command Message (AMF0) | コマンド（connect, publish, playなど） |

## プロトコル制御メッセージの詳細

### TypeID 1: Set Chunk Size

チャンクの最大サイズを変更する。デフォルト128バイトから4096バイトなどに変更する際に使う。

```
+---+-------------------------------+
| 0 |       chunk size              |
+---+-------------------------------+
 1bit        31bits (BE)

最上位ビットは常に0（予約）
有効範囲: 1 〜 0x7FFFFFFF
```

例: チャンクサイズ4096に変更
```
00 00 10 00  (= 4096)
```

**対応するソースコード:**
```go
func NewSetChunkSize(size uint32) (chunk.Header, []byte) {
    body := make([]byte, 4)
    binary.BigEndian.PutUint32(body, size&0x7FFFFFFF) // 最上位ビットをクリア
    // ...
}
```

### TypeID 3: Acknowledgement

フロー制御用の受信確認応答。指定バイト数を受信するたびに送信する。

```
+-------------------------------+
|     sequence number           |
|      (4 bytes, BE)            |
+-------------------------------+

sequence number: これまでに受信した累計バイト数
```

### TypeID 5: Window Acknowledgement Size

Acknowledgementを送信する間隔（バイト数）を設定する。

```
+-------------------------------+
| acknowledgement window size   |
|      (4 bytes, BE)            |
+-------------------------------+
```

例: 2,500,000バイトごとにAckを返す
```
00 26 25 A0  (= 2,500,000)
```

### TypeID 6: Set Peer Bandwidth

相手の送信帯域幅の上限を設定する。

```
+-------------------------------+----------+
| acknowledgement window size   | limit    |
|      (4 bytes, BE)            | type     |
+-------------------------------+----------+
              4バイト               1バイト
```

| limit type | 値 | 意味 |
|---|---|---|
| Hard | 0 | 指定値を超えてはならない |
| Soft | 1 | 現在の値と指定値の小さい方 |
| Dynamic | 2 | 前回がHardならSoftとして扱う |

### TypeID 4: User Control Message

ストリームの状態を通知するイベントメッセージ。

```
+------------------+------------------+
| event type       | event data       |
| (2 bytes, BE)    | (variable)       |
+------------------+------------------+
```

| イベントタイプ | 値 | データ | 説明 |
|---|---|---|---|
| Stream Begin | 0 | streamID (4B) | ストリームが利用可能 |
| Stream EOF | 1 | streamID (4B) | ストリーム終了 |
| Stream Dry | 2 | streamID (4B) | データなし |
| Set Buffer Length | 3 | streamID (4B) + length (4B) | バッファサイズ設定 |
| Stream Is Recorded | 4 | streamID (4B) | 録画中 |
| Ping Request | 6 | timestamp (4B) | キープアライブ要求 |
| Ping Response | 7 | timestamp (4B) | キープアライブ応答 |

**Stream Begin** は配信開始時にサーバーからクライアントに送られる。
publishに対する応答の一部として送信される。

## コマンドメッセージ（TypeID 20）

AMF0でエンコードされたRPCスタイルのコマンド。RTMPのアプリケーションロジックの中心。

### コマンドの一般的な構造

```
[String: コマンド名] [Number: トランザクションID] [パラメータ...]
```

### トランザクションIDの役割

要求と応答を対応付けるための番号。

```
クライアント: connect (txnID=1) ────>
                                    <──── _result (txnID=1)

クライアント: createStream (txnID=2) ──>
                                    <──── _result (txnID=2)
```

応答不要のコマンド（publishなど）はtxnID=0を使う。

### 主要なコマンド

#### connect

アプリケーションへの接続を要求する。

```
要求: "connect", 1.0, {app: "live", type: "nonprivate", flashVer: "...", tcUrl: "..."}
応答: "_result", 1.0, {fmsVer: "...", capabilities: 31}, {level: "status", code: "NetConnection.Connect.Success"}
```

#### createStream

メッセージストリームIDの割り当てを要求する。

```
要求: "createStream", 2.0, null
応答: "_result", 2.0, null, 1.0    ← streamID=1が割り当てられた
```

#### publish

配信開始を宣言する。

```
要求: "publish", 0.0, null, "streamkey", "live"
応答: "onStatus", 0.0, null, {level: "status", code: "NetStream.Publish.Start"}
```

#### play

視聴を要求する。

```
要求: "play", 0.0, null, "streamkey"
応答: "onStatus", 0.0, null, {level: "status", code: "NetStream.Play.Start"}
```

#### deleteStream

ストリームを削除（配信/視聴を終了）する。

```
要求: "deleteStream", 0.0, null, 1.0  ← streamID=1を削除
```

## 音声メッセージ（TypeID 8）の構造

```
+---+---+---+---+---+------...------+
| codec | rate|sz |ch| audio data   |
+---+---+---+---+---+------...------+
  4bit   2bit 1bit 1bit

先頭1バイトの内訳:
  上位4ビット: コーデック種別
    2  = MP3
    10 = AAC    ← 最も一般的
    11 = Speex
  ビット3-2: サンプルレート
    0=5.5kHz, 1=11kHz, 2=22kHz, 3=44kHz
  ビット1: サンプルサイズ
    0=8bit, 1=16bit
  ビット0: チャンネル
    0=モノラル, 1=ステレオ
```

### AACの場合

AACでは2バイト目に **AACパケットタイプ** が付く:

| 値 | 名前 | 説明 |
|---|---|---|
| 0 | AAC Sequence Header | デコーダ設定情報（AudioSpecificConfig） |
| 1 | AAC Raw | 実際の音声データ |

**Sequence Header（設定情報）** は配信の最初に1回だけ送られ、
デコーダがAACストリームを正しくデコードするために必要な情報を含む。
サーバーはこれをキャッシュして、途中参加の視聴者にも送信する。

## 映像メッセージ（TypeID 9）の構造

```
+--------+--------+------...------+
|frame|codec| video data          |
+--------+--------+------...------+
  4bit  4bit

先頭1バイトの内訳:
  上位4ビット: フレームタイプ
    1 = キーフレーム（I-frame、独立してデコード可能）
    2 = インターフレーム（P-frame、前のフレームとの差分）
  下位4ビット: コーデック種別
    7 = H.264/AVC    ← 最も一般的
```

### H.264/AVCの場合

2バイト目に **AVCパケットタイプ** が付く:

| 値 | 名前 | 説明 |
|---|---|---|
| 0 | AVC Sequence Header | SPS/PPSパラメータセット（デコーダ設定） |
| 1 | AVC NALU | 実際の映像データ（NAL Unit） |
| 2 | AVC End of Sequence | シーケンス終了 |

**Sequence Header** には映像のプロファイル、レベル、解像度などの
デコーダ設定情報（SPS: Sequence Parameter Set, PPS: Picture Parameter Set）が含まれる。
これも配信の最初に送られ、サーバーがキャッシュする。

**対応するソースコード（サーバー側でのヘッダ検出）:**
```go
// server/server.go - handleMediaMessage 内
case message.TypeAudioMessage:
    // AACのSequence Headerかチェック: コーデック=AAC(10) かつ パケットタイプ=0
    if len(body) >= 2 && (body[0]>>4) == 10 && body[1] == 0 {
        s.audioHeader = make([]byte, len(body))
        copy(s.audioHeader, body)
    }
case message.TypeVideoMessage:
    // AVCのSequence Headerかチェック: コーデック=AVC(7) かつ パケットタイプ=0
    if len(body) >= 2 && (body[0]&0x0F) == 7 && body[1] == 0 {
        s.videoHeader = make([]byte, len(body))
        copy(s.videoHeader, body)
    }
```

## メッセージとチャンクストリームIDの対応

| メッセージ種別 | 使うcsid | 理由 |
|---|---|---|
| プロトコル制御（TypeID 1-6） | 2 | 仕様で固定 |
| コマンド・データ（TypeID 18, 20） | 3 | 慣例 |
| 音声（TypeID 8） | 4 | 慣例 |
| 映像（TypeID 9） | 6 | 慣例 |

異なるcsidを使うことで、チャンクストリーム層が各ストリームの状態を
独立に管理でき、ヘッダ圧縮も効率的に行える。
