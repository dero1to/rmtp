# 07. サーバー・クライアント実装詳解

> 対応ソースコード: `server/server.go`, `client/client.go`

## アーキテクチャ概要

```
┌──────────────────────────────────────────────────────────────┐
│                        RTMPサーバー                            │
│                                                               │
│  ┌─────────┐    ┌──────────────────────────────────────────┐ │
│  │ TCP     │    │           接続ハンドラ (goroutine)        │ │
│  │ Listener│───>│                                          │ │
│  │ :1935   │    │  Handshake → ChunkReader/Writer → ...    │ │
│  └─────────┘    └──────────────────────────────────────────┘ │
│                                                               │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │                   ストリーム管理                          │ │
│  │                                                          │ │
│  │  streams map[string]*stream                              │ │
│  │    └── "streamkey" → {                                   │ │
│  │          publisher:   *conn     (配信者)                  │ │
│  │          subscribers: map[*conn] (視聴者一覧)             │ │
│  │          metadata:    []byte    (キャッシュ)              │ │
│  │          audioHeader: []byte    (AAC設定)                 │ │
│  │          videoHeader: []byte    (AVC設定)                 │ │
│  │        }                                                  │ │
│  └──────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

## サーバーの全体フロー

### 1. 起動と接続待ち受け

```go
func (s *Server) ListenAndServe() error {
    listener, _ := net.Listen("tcp", s.config.Addr)
    for {
        netConn, _ := listener.Accept()
        go s.handleConnection(netConn) // goroutineで並行処理
    }
}
```

各クライアント接続は独立したgoroutineで処理される。
Go の goroutine は軽量（数KB程度）なので、数百〜数千の同時接続にも対応できる。

### 2. 接続ハンドラの処理フロー

```
handleConnection(netConn)
    │
    ├── ① ServerHandshake(netConn)
    │   └── C0C1読み取り → S0S1S2送信 → C2読み取り
    │
    ├── ② ChunkReader/Writer 初期化
    │
    └── ③ メッセージループ (for)
        │
        ├── reader.ReadMessage()
        │   └── チャンクを読み取りメッセージを再構築
        │
        └── handleMessage(hdr, body)
            │
            ├── TypeID=1:  handleSetChunkSize()
            ├── TypeID=20: handleCommandMessage()
            │   ├── "connect"      → handleConnect()
            │   ├── "createStream" → handleCreateStream()
            │   ├── "publish"      → handlePublish()
            │   ├── "play"         → handlePlay()
            │   └── "deleteStream" → handleDeleteStream()
            ├── TypeID=18: handleDataMessage()
            ├── TypeID=8:  handleMediaMessage() (音声)
            └── TypeID=9:  handleMediaMessage() (映像)
```

## サーバーが送信する制御メッセージ

### connect への応答

サーバーはconnectコマンドに対して **4つのメッセージ** を送る:

```
1. Window Acknowledgement Size (2,500,000バイト)
   → クライアントに「250万バイトごとにAckを返して」と要求

2. Set Peer Bandwidth (2,500,000バイト, Dynamic)
   → クライアントの送信帯域幅上限を設定

3. Set Chunk Size (4096)
   → チャンクサイズを128→4096に変更して効率化

4. _result (成功レスポンス)
   → connectの成功を通知（プロパティ + 結果情報オブジェクト）
```

**なぜ2,500,000バイト？**

これはAdobe Flash Media Serverのデフォルト値で、多くの実装が踏襲している。
約2.5MBごとにフロー制御を行う設定。

**なぜチャンクサイズ4096？**

デフォルトの128バイトでは、映像フレーム（数十KB〜数百KB）を送ると
大量のチャンクヘッダが発生してオーバーヘッドが大きい。
4096バイトにすると、ヘッダの回数が約32分の1になる。

### connect応答のAMF0構造

```go
// サーバーのプロパティオブジェクト
props := amf0.NewObject()
props.Set("fmsVer", "FMS/3,0,1,123")    // Flash Media Server互換
props.Set("capabilities", float64(31))    // サーバー機能フラグ

// 接続結果の情報オブジェクト
info := amf0.NewObject()
info.Set("level", "status")
info.Set("code", "NetConnection.Connect.Success")
info.Set("description", "Connection succeeded.")
info.Set("objectEncoding", float64(0))   // AMF0使用

// _result を送信
message.NewCommandMessage(0, "_result", txnID, props, info)
```

## 配信（publish）の処理詳細

### 配信者がpublishを送信したとき

```
handlePublish()
    │
    ├── ストリームキーを取得 (values[3])
    │
    ├── ストリーム登録
    │   └── server.streams[streamKey] = &stream{publisher: thisConn}
    │
    ├── FLV保存の初期化（設定されている場合）
    │   └── initFLVWriter() → ファイル作成 + FLVヘッダ書き込み
    │
    ├── Stream Begin 送信（ユーザー制御メッセージ）
    │
    └── onStatus 送信
        └── "NetStream.Publish.Start"
```

### メディアデータの受信と中継

```
handleMediaMessage(hdr, body)
    │
    ├── Sequence Headerの検出とキャッシュ
    │   ├── AAC: (body[0]>>4)==10 && body[1]==0 → audioHeader に保存
    │   └── AVC: (body[0]&0x0F)==7 && body[1]==0 → videoHeader に保存
    │
    ├── FLV書き込み（録画が有効な場合）
    │   └── flvWriter.WriteTag(tagType, timestamp, body)
    │
    └── 視聴者への中継
        └── relayToSubscribers(hdr, body)
            └── 全視聴者に対して writer.WriteMessage()
```

## 視聴（play）の処理詳細

### 途中参加対応

視聴者が配信途中で接続してきた場合、そのままメディアデータを送っても
デコーダが映像/音声を正しくデコードできない。
なぜなら、H.264やAACのデコーダは **Sequence Header** がないと動作しないから。

```
handlePlay()
    │
    ├── 視聴者リストに追加
    │   └── stream.subscribers[thisConn] = struct{}{}
    │
    ├── Stream Begin + onStatus 送信
    │
    └── キャッシュされたヘッダ情報を送信
        ├── metadata (onMetaData)      ← 解像度やフレームレートの情報
        ├── audioHeader (AAC Seq Header) ← 音声デコーダ設定
        └── videoHeader (AVC Seq Header) ← 映像デコーダ設定
```

これにより、途中参加の視聴者も即座にデコードを開始できる。

## クライアントの全体フロー

### Publish() 関数の処理

```
Publish(addr, app, streamKey)
    │
    ├── ① TCP接続 (10秒タイムアウト)
    │   └── net.DialTimeout("tcp", addr, 10s)
    │
    ├── ② ハンドシェイク
    │   └── ClientHandshake(conn)
    │
    ├── ③ connect送信
    │   └── sendConnect()
    │       └── AMF0: "connect", 1.0, {app: "live", ...}
    │
    ├── ④ サーバー応答待ち
    │   └── readServerResponses()
    │       ├── Window Ack Size → 受信するだけ
    │       ├── Set Peer Bandwidth → 受信するだけ
    │       ├── Set Chunk Size → reader.SetChunkSize() で反映
    │       └── _result → connect成功確認、ループ終了
    │
    ├── ⑤ createStream送信
    │   └── sendCreateStream()
    │       └── AMF0: "createStream", 2.0, null
    │
    ├── ⑥ createStream応答待ち
    │   └── readCreateStreamResult()
    │       └── _result の values[3] からstreamIDを取得
    │
    ├── ⑦ publish送信
    │   └── sendPublish()
    │       └── AMF0: "publish", 0.0, null, streamKey, "live"
    │
    └── ⑧ publish応答待ち
        └── readUntilPublishStart()
            └── onStatus を受信したらループ終了
```

### FLV配信のペース制御

```go
func (c *Client) PublishFLV(r io.Reader) error {
    var startTime time.Time
    var firstTimestamp uint32

    for {
        tag, _ := flvReader.ReadTag()

        if !started {
            // 最初のタグ: 基準時刻を記録
            startTime = time.Now()
            firstTimestamp = tag.Timestamp
        } else {
            // 経過すべき時間を計算
            elapsed := time.Duration(tag.Timestamp - firstTimestamp) * time.Millisecond
            // 実際の経過時間との差分だけスリープ
            wait := elapsed - time.Since(startTime)
            if wait > 0 {
                time.Sleep(wait)
            }
        }

        // FLVタグをRTMPメッセージとして送信
        switch tag.TagType {
        case flv.TagTypeAudio:  c.SendAudio(tag.Timestamp, tag.Data)
        case flv.TagTypeVideo:  c.SendVideo(tag.Timestamp, tag.Data)
        case flv.TagTypeScript: // メタデータとして送信
        }
    }
}
```

**なぜペース制御が必要か？**

FLVファイルからの読み取りは瞬時に行えるが、もし全データを一気に送信すると:
- サーバーのバッファが溢れる
- 視聴者側でバッファリングの問題が起きる
- タイムスタンプと実時間の関係が崩れる

元のFLVのタイムスタンプに従って「リアルタイム速度」で送信することで、
本物のライブ配信と同じ挙動になる。

## スレッドセーフティ

### サーバー側のロック戦略

```
Server.mu (RWMutex)
  └── streams mapへのアクセスを保護

stream.mu (RWMutex)
  └── publisher, subscribers, metadata, headerへのアクセスを保護
```

- **読み取りが多い操作**（メディアデータの中継）は `RLock` を使い、
  複数goroutineが同時に読み取れるようにしている
- **書き込み操作**（配信者/視聴者の追加/削除）は `Lock` で排他制御

### 具体例: メディアデータの中継

```go
func (c *conn) relayToSubscribers(hdr chunk.Header, body []byte) {
    c.server.mu.RLock()    // ストリーム検索は読み取りロック
    s, ok := c.server.streams[c.streamKey]
    c.server.mu.RUnlock()

    s.mu.RLock()           // 視聴者一覧の走査も読み取りロック
    defer s.mu.RUnlock()
    for sub := range s.subscribers {
        sub.writer.WriteMessage(relayHdr, body) // 各視聴者に送信
    }
}
```

## クリーンアップ処理

接続が切れた際の処理:

```
cleanup()
    │
    ├── FLVファイルを閉じる
    │   └── c.flvFile.Close()
    │
    └── ストリームから自分を削除
        ├── 配信者の場合:
        │   └── s.publisher = nil
        │       └── ストリーム自体を削除（視聴者全員が見れなくなる）
        │
        └── 視聴者の場合:
            └── delete(s.subscribers, c)
```

## サーバー/クライアントが使うパッケージの依存関係

```
cmd/server/main.go
    └── server/server.go
        ├── pkg/handshake/   (ハンドシェイク)
        ├── pkg/chunk/       (チャンク読み書き)
        ├── pkg/amf0/        (コマンドのデコード/エンコード)
        ├── pkg/message/     (メッセージ定数・構築関数)
        └── pkg/flv/         (FLV録画)

cmd/client/main.go
    └── client/client.go
        ├── pkg/handshake/
        ├── pkg/chunk/
        ├── pkg/amf0/
        ├── pkg/message/
        └── pkg/flv/         (FLVファイル読み取り)
```

すべてのパッケージが標準ライブラリのみに依存しており、
外部ライブラリは一切使っていない。
