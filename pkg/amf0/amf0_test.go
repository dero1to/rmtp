package amf0

import (
	"testing"
)

// TestEncodeDecodeNumber はNumber型のエンコード/デコードをテストする。
func TestEncodeDecodeNumber(t *testing.T) {
	tests := []float64{0, 1, -1, 3.14, 1000000, 0.001}
	for _, expected := range tests {
		data, err := Encode(expected)
		if err != nil {
			t.Fatalf("エンコードエラー: %v", err)
		}
		values, err := Decode(data)
		if err != nil {
			t.Fatalf("デコードエラー: %v", err)
		}
		if len(values) != 1 {
			t.Fatalf("値の数が不正: got %d, want 1", len(values))
		}
		if got, ok := values[0].(float64); !ok || got != expected {
			t.Errorf("値が不正: got %v, want %v", values[0], expected)
		}
	}
}

// TestEncodeDecodeString は文字列型のエンコード/デコードをテストする。
func TestEncodeDecodeString(t *testing.T) {
	tests := []string{"", "hello", "connect", "日本語テスト"}
	for _, expected := range tests {
		data, err := Encode(expected)
		if err != nil {
			t.Fatalf("エンコードエラー: %v", err)
		}
		values, err := Decode(data)
		if err != nil {
			t.Fatalf("デコードエラー: %v", err)
		}
		if len(values) != 1 {
			t.Fatalf("値の数が不正: got %d, want 1", len(values))
		}
		if got, ok := values[0].(string); !ok || got != expected {
			t.Errorf("値が不正: got %v, want %v", values[0], expected)
		}
	}
}

// TestEncodeDecodeBoolean はBoolean型のエンコード/デコードをテストする。
func TestEncodeDecodeBoolean(t *testing.T) {
	for _, expected := range []bool{true, false} {
		data, err := Encode(expected)
		if err != nil {
			t.Fatalf("エンコードエラー: %v", err)
		}
		values, err := Decode(data)
		if err != nil {
			t.Fatalf("デコードエラー: %v", err)
		}
		if got, ok := values[0].(bool); !ok || got != expected {
			t.Errorf("値が不正: got %v, want %v", values[0], expected)
		}
	}
}

// TestEncodeDecodeNull はNull型のエンコード/デコードをテストする。
func TestEncodeDecodeNull(t *testing.T) {
	data, err := Encode(nil)
	if err != nil {
		t.Fatalf("エンコードエラー: %v", err)
	}
	values, err := Decode(data)
	if err != nil {
		t.Fatalf("デコードエラー: %v", err)
	}
	if values[0] != nil {
		t.Errorf("null を期待したが %v が返った", values[0])
	}
}

// TestEncodeDecodeObject はObject型のエンコード/デコードをテストする。
func TestEncodeDecodeObject(t *testing.T) {
	obj := NewObject()
	obj.Set("app", "live")
	obj.Set("flashVer", "FMLE/3.0")
	obj.Set("type", "nonprivate")

	data, err := Encode(obj)
	if err != nil {
		t.Fatalf("エンコードエラー: %v", err)
	}
	values, err := Decode(data)
	if err != nil {
		t.Fatalf("デコードエラー: %v", err)
	}

	decoded, ok := values[0].(*Object)
	if !ok {
		t.Fatalf("Object型を期待したが %T が返った", values[0])
	}

	// キーの数を確認
	if len(decoded.Keys) != 3 {
		t.Errorf("キーの数が不正: got %d, want 3", len(decoded.Keys))
	}

	// 値を確認
	if v, _ := decoded.Get("app"); v != "live" {
		t.Errorf("app の値が不正: got %v, want live", v)
	}
	if v, _ := decoded.Get("flashVer"); v != "FMLE/3.0" {
		t.Errorf("flashVer の値が不正: got %v, want FMLE/3.0", v)
	}
}

// TestEncodeDecodeMultipleValues は複数値の連続エンコード/デコードをテストする。
// RTMPのコマンドメッセージは複数のAMF0値が連続して格納される。
func TestEncodeDecodeMultipleValues(t *testing.T) {
	// connectコマンドをシミュレート
	obj := NewObject()
	obj.Set("app", "live")

	data, err := Encode("connect", float64(1), obj)
	if err != nil {
		t.Fatalf("エンコードエラー: %v", err)
	}

	values, err := Decode(data)
	if err != nil {
		t.Fatalf("デコードエラー: %v", err)
	}

	if len(values) != 3 {
		t.Fatalf("値の数が不正: got %d, want 3", len(values))
	}

	if values[0] != "connect" {
		t.Errorf("コマンド名が不正: got %v", values[0])
	}
	if values[1] != float64(1) {
		t.Errorf("トランザクションIDが不正: got %v", values[1])
	}
	if decoded, ok := values[2].(*Object); !ok {
		t.Errorf("Object型を期待: got %T", values[2])
	} else {
		if v, _ := decoded.Get("app"); v != "live" {
			t.Errorf("app の値が不正: got %v", v)
		}
	}
}
