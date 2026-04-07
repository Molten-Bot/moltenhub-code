package hub

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDialWebsocketSuccessAndAuthHeader(t *testing.T) {
	t.Parallel()

	authHeaderCh := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaderCh <- r.Header.Get("Authorization")
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("response writer does not support hijack")
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Fatalf("Hijack() error = %v", err)
		}
		key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
		_, _ = fmt.Fprintf(
			rw,
			"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			websocketAccept(key),
		)
		_ = rw.Flush()
		_ = conn.Close()
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	client, err := DialWebsocket(context.Background(), wsURL, "token-123")
	if err != nil {
		t.Fatalf("DialWebsocket() error = %v", err)
	}
	defer client.Close()

	select {
	case got := <-authHeaderCh:
		if got != "Bearer token-123" {
			t.Fatalf("Authorization header = %q, want %q", got, "Bearer token-123")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request header capture")
	}
}

func TestDialWebsocketValidationFailures(t *testing.T) {
	t.Parallel()

	if _, err := DialWebsocket(context.Background(), "https://example.com/ws", ""); err == nil {
		t.Fatal("DialWebsocket(https URL) error = nil, want non-nil")
	}

	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer statusServer.Close()
	if _, err := DialWebsocket(context.Background(), "ws"+strings.TrimPrefix(statusServer.URL, "http"), ""); err == nil {
		t.Fatal("DialWebsocket(handshake status error) error = nil, want non-nil")
	}

	missingHeaderServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, rw, _ := hj.Hijack()
		key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
		_, _ = fmt.Fprintf(
			rw,
			"HTTP/1.1 101 Switching Protocols\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			websocketAccept(key),
		)
		_ = rw.Flush()
		_ = conn.Close()
	}))
	defer missingHeaderServer.Close()
	if _, err := DialWebsocket(context.Background(), "ws"+strings.TrimPrefix(missingHeaderServer.URL, "http"), ""); err == nil {
		t.Fatal("DialWebsocket(missing upgrade headers) error = nil, want non-nil")
	}

	badAcceptServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, rw, _ := hj.Hijack()
		_, _ = fmt.Fprintf(
			rw,
			"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: bad-value\r\n\r\n",
		)
		_ = rw.Flush()
		_ = conn.Close()
	}))
	defer badAcceptServer.Close()
	if _, err := DialWebsocket(context.Background(), "ws"+strings.TrimPrefix(badAcceptServer.URL, "http"), ""); err == nil {
		t.Fatal("DialWebsocket(invalid accept key) error = nil, want non-nil")
	}
}

func TestWSClientWriteAndClosePaths(t *testing.T) {
	t.Parallel()

	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	client := &WSClient{conn: clientConn, reader: bufio.NewReader(clientConn)}
	defer client.Close()

	frameCh := make(chan frameData, 1)
	errCh := make(chan error, 1)
	go func() {
		frame, err := readFrameFromConn(serverConn)
		if err != nil {
			errCh <- err
			return
		}
		frameCh <- frame
	}()

	if err := client.WriteJSON(map[string]any{"ok": true}); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("server read frame error = %v", err)
	case frame := <-frameCh:
		if frame.opcode != opcodeText {
			t.Fatalf("frame opcode = %d, want %d", frame.opcode, opcodeText)
		}
		var decoded map[string]any
		if err := json.Unmarshal(frame.payload, &decoded); err != nil {
			t.Fatalf("Unmarshal(frame payload) error = %v", err)
		}
		if got, ok := decoded["ok"].(bool); !ok || !got {
			t.Fatalf("decoded payload = %#v, want ok=true", decoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for frame from client")
	}

	if err := client.WritePing(make([]byte, 126)); err == nil {
		t.Fatal("WritePing(oversized control payload) error = nil, want non-nil")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := client.WriteJSON(map[string]any{"after": "close"}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("WriteJSON(after close) error = %v, want %v", err, io.ErrClosedPipe)
	}
}

func TestWSClientReadJSONHandlesPingBinaryAndText(t *testing.T) {
	t.Parallel()

	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	client := &WSClient{conn: clientConn, reader: bufio.NewReader(clientConn)}

	serverErrCh := make(chan error, 1)
	go func() {
		if err := writeFrameToConn(serverConn, true, opcodePing, []byte("hi"), false); err != nil {
			serverErrCh <- err
			return
		}
		pong, err := readFrameFromConn(serverConn)
		if err != nil {
			serverErrCh <- err
			return
		}
		if pong.opcode != opcodePong || string(pong.payload) != "hi" {
			serverErrCh <- fmt.Errorf("unexpected pong frame: %+v", pong)
			return
		}
		if err := writeFrameToConn(serverConn, true, opcodeBinary, []byte{1, 2, 3}, false); err != nil {
			serverErrCh <- err
			return
		}
		if err := writeFrameToConn(serverConn, true, opcodeText, []byte(`{"value":"ok"}`), false); err != nil {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	var decoded map[string]string
	if err := client.ReadJSON(&decoded); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	if got, want := decoded["value"], "ok"; got != want {
		t.Fatalf("decoded value = %q, want %q", got, want)
	}

	if err := <-serverErrCh; err != nil {
		t.Fatalf("server goroutine error = %v", err)
	}
}

func TestWSClientReadJSONErrorCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fin     bool
		opcode  byte
		payload []byte
		wantErr string
	}{
		{name: "fragmented text", fin: false, opcode: opcodeText, payload: []byte(`{"ok":true}`), wantErr: "fragmented text frames"},
		{name: "continuation", fin: true, opcode: opcodeContinuation, payload: []byte("x"), wantErr: "unexpected continuation"},
		{name: "close", fin: true, opcode: opcodeClose, payload: nil, wantErr: "EOF"},
		{name: "invalid json", fin: true, opcode: opcodeText, payload: []byte("not-json"), wantErr: "decode websocket json"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clientConn, serverConn := net.Pipe()
			defer serverConn.Close()
			defer clientConn.Close()

			client := &WSClient{conn: clientConn, reader: bufio.NewReader(clientConn)}
			go func() {
				_ = writeFrameToConn(serverConn, tt.fin, tt.opcode, tt.payload, false)
			}()

			var decoded map[string]any
			err := client.ReadJSON(&decoded)
			if err == nil {
				t.Fatalf("ReadJSON() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ReadJSON() error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestReadFrameParsesMaskedPayloadAndValidationErrors(t *testing.T) {
	t.Parallel()

	masked := buildFrameBytes(true, opcodeText, []byte("hello"), true, [4]byte{1, 2, 3, 4})
	client := &WSClient{reader: bufio.NewReader(strings.NewReader(string(masked)))}
	fin, opcode, payload, err := client.readFrame()
	if err != nil {
		t.Fatalf("readFrame(masked) error = %v", err)
	}
	if !fin || opcode != opcodeText || string(payload) != "hello" {
		t.Fatalf("readFrame(masked) = fin=%v opcode=%d payload=%q", fin, opcode, string(payload))
	}

	fragmentedControl := buildFrameBytes(false, opcodePing, []byte("a"), false, [4]byte{})
	client = &WSClient{reader: bufio.NewReader(strings.NewReader(string(fragmentedControl)))}
	if _, _, _, err := client.readFrame(); err == nil || !strings.Contains(err.Error(), "fragmented control frame") {
		t.Fatalf("readFrame(fragmented control) error = %v", err)
	}

	oversizedControl := append([]byte{0x80 | opcodePing, 126, 0, 126}, make([]byte, 126)...)
	client = &WSClient{reader: bufio.NewReader(strings.NewReader(string(oversizedControl)))}
	if _, _, _, err := client.readFrame(); err == nil || !strings.Contains(err.Error(), "oversized control frame") {
		t.Fatalf("readFrame(oversized control) error = %v", err)
	}

	var huge [10]byte
	huge[0] = 0x80 | opcodeText
	huge[1] = 127
	binary.BigEndian.PutUint64(huge[2:], uint64(math.MaxInt32)+1)
	client = &WSClient{reader: bufio.NewReader(strings.NewReader(string(huge[:])))}
	if _, _, _, err := client.readFrame(); err == nil || !strings.Contains(err.Error(), "frame too large") {
		t.Fatalf("readFrame(too large) error = %v", err)
	}
}

func TestHeaderAndWebsocketHelpers(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"Connection": []string{"keep-alive, Upgrade"},
		"Upgrade":    []string{"websocket"},
	}
	if !headerHasToken(headers, "Connection", "upgrade") {
		t.Fatal("headerHasToken(Connection, upgrade) = false, want true")
	}
	if headerHasToken(headers, "Connection", "close") {
		t.Fatal("headerHasToken(Connection, close) = true, want false")
	}

	key := randomSecWebsocketKey()
	if strings.TrimSpace(key) == "" {
		t.Fatal("randomSecWebsocketKey() returned empty key")
	}
	if got, want := websocketAccept("dGhlIHNhbXBsZSBub25jZQ=="), "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="; got != want {
		t.Fatalf("websocketAccept(sample) = %q, want %q", got, want)
	}
}

type frameData struct {
	fin     bool
	opcode  byte
	payload []byte
}

func writeFrameToConn(w io.Writer, fin bool, opcode byte, payload []byte, masked bool) error {
	frame := buildFrameBytes(fin, opcode, payload, masked, [4]byte{9, 8, 7, 6})
	_, err := w.Write(frame)
	return err
}

func buildFrameBytes(fin bool, opcode byte, payload []byte, masked bool, maskKey [4]byte) []byte {
	first := opcode & 0x0F
	if fin {
		first |= 0x80
	}

	out := make([]byte, 0, len(payload)+14)
	out = append(out, first)

	length := len(payload)
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch {
	case length <= 125:
		out = append(out, maskBit|byte(length))
	case length <= math.MaxUint16:
		out = append(out, maskBit|126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(length))
		out = append(out, ext[:]...)
	default:
		out = append(out, maskBit|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		out = append(out, ext[:]...)
	}

	if !masked {
		out = append(out, payload...)
		return out
	}

	out = append(out, maskKey[:]...)
	for i := range payload {
		out = append(out, payload[i]^maskKey[i%4])
	}
	return out
}

func readFrameFromConn(r io.Reader) (frameData, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return frameData{}, err
	}
	fin := head[0]&0x80 != 0
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int(head[1] & 0x7F)

	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frameData{}, err
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	} else if length == 127 {
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frameData{}, err
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return frameData{}, err
		}
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return frameData{}, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return frameData{fin: fin, opcode: opcode, payload: payload}, nil
}
