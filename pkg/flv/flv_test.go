package flv

import (
	"bytes"
	"testing"
)

// TestFLVWriteRead はFLVの書き込みと読み取りをテストする。
func TestFLVWriteRead(t *testing.T) {
	buf := new(bytes.Buffer)

	// FLVファイルを書き込み
	writer, err := NewWriter(buf, true, true)
	if err != nil {
		t.Fatalf("Writer作成エラー: %v", err)
	}

	// テスト用のタグを書き込み
	audioData := []byte{0xAF, 0x00, 0x12, 0x10} // AAC Sequence Header風
	videoData := []byte{0x17, 0x00, 0x00, 0x00, 0x00} // AVC Sequence Header風

	if err := writer.WriteTag(TagTypeAudio, 0, audioData); err != nil {
		t.Fatalf("音声タグ書き込みエラー: %v", err)
	}
	if err := writer.WriteTag(TagTypeVideo, 0, videoData); err != nil {
		t.Fatalf("映像タグ書き込みエラー: %v", err)
	}
	if err := writer.WriteTag(TagTypeAudio, 23, []byte{0xAF, 0x01, 0xDE, 0xAD}); err != nil {
		t.Fatalf("音声タグ書き込みエラー: %v", err)
	}

	// 読み取り
	reader, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Reader作成エラー: %v", err)
	}

	// タグ1: 音声
	tag1, err := reader.ReadTag()
	if err != nil {
		t.Fatalf("タグ1読み取りエラー: %v", err)
	}
	if tag1.TagType != TagTypeAudio {
		t.Errorf("タグ1のタイプが不正: got %d, want %d", tag1.TagType, TagTypeAudio)
	}
	if tag1.Timestamp != 0 {
		t.Errorf("タグ1のタイムスタンプが不正: got %d, want 0", tag1.Timestamp)
	}
	if !bytes.Equal(tag1.Data, audioData) {
		t.Errorf("タグ1のデータが不正")
	}

	// タグ2: 映像
	tag2, err := reader.ReadTag()
	if err != nil {
		t.Fatalf("タグ2読み取りエラー: %v", err)
	}
	if tag2.TagType != TagTypeVideo {
		t.Errorf("タグ2のタイプが不正: got %d, want %d", tag2.TagType, TagTypeVideo)
	}

	// タグ3: 音声（タイムスタンプ付き）
	tag3, err := reader.ReadTag()
	if err != nil {
		t.Fatalf("タグ3読み取りエラー: %v", err)
	}
	if tag3.Timestamp != 23 {
		t.Errorf("タグ3のタイムスタンプが不正: got %d, want 23", tag3.Timestamp)
	}
}
