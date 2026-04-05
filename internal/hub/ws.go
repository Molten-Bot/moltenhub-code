package hub

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const (
	opcodeContinuation = 0x0
	opcodeText         = 0x1
	opcodeBinary       = 0x2
	opcodeClose        = 0x8
	opcodePing         = 0x9
	opcodePong         = 0xA
)

const websocketMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WSClient is a minimal websocket client for text JSON messages.
type WSClient struct {
	conn     net.Conn
	reader   *bufio.Reader
	writeMu  sync.Mutex
	closeMu  sync.Mutex
	isClosed bool
}

// DialWebsocket creates an authenticated websocket connection.
func DialWebsocket(ctx context.Context, rawURL, bearerToken string) (*WSClient, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse websocket url: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("websocket url must use ws or wss")
	}

	addr := u.Host
	if !strings.Contains(addr, ":") {
		if u.Scheme == "wss" {
			addr += ":443"
		} else {
			addr += ":80"
		}
	}

	dialer := &net.Dialer{}
	var conn net.Conn
	if u.Scheme == "wss" {
		tlsCfg := &tls.Config{ServerName: u.Hostname()}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial websocket: %w", err)
	}

	key := randomSecWebsocketKey()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("build handshake request: %w", err)
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("User-Agent", "moltenhub-code/1")
	if strings.TrimSpace(bearerToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
	}

	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write handshake request: %w", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read handshake response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake status=%d body=%s", resp.StatusCode, truncateBody(body))
	}

	if !headerHasToken(resp.Header, "Connection", "upgrade") || !headerHasToken(resp.Header, "Upgrade", "websocket") {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake missing upgrade headers")
	}

	accept := strings.TrimSpace(resp.Header.Get("Sec-WebSocket-Accept"))
	if !strings.EqualFold(accept, websocketAccept(key)) {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake invalid accept key")
	}

	return &WSClient{conn: conn, reader: reader}, nil
}

// Close closes the websocket connection.
func (c *WSClient) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.isClosed {
		return nil
	}
	c.isClosed = true
	return c.conn.Close()
}

// ReadJSON reads the next text frame and decodes JSON.
func (c *WSClient) ReadJSON(v any) error {
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return err
		}

		switch opcode {
		case opcodeText:
			if !fin {
				return errors.New("fragmented text frames are not supported")
			}
			if err := json.Unmarshal(payload, v); err != nil {
				return fmt.Errorf("decode websocket json: %w", err)
			}
			return nil
		case opcodePing:
			if err := c.writeFrame(opcodePong, payload); err != nil {
				return err
			}
		case opcodePong:
			continue
		case opcodeBinary:
			continue
		case opcodeContinuation:
			return errors.New("unexpected continuation frame")
		case opcodeClose:
			return io.EOF
		default:
			continue
		}
	}
}

// WriteJSON encodes and writes a websocket text frame.
func (c *WSClient) WriteJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode websocket json: %w", err)
	}
	return c.writeFrame(opcodeText, payload)
}

// WritePing writes a ping control frame.
func (c *WSClient) WritePing(payload []byte) error {
	return c.writeFrame(opcodePing, payload)
}

func (c *WSClient) writeFrame(opcode byte, payload []byte) error {
	if opcode >= opcodeClose && len(payload) > 125 {
		return fmt.Errorf("control frame payload exceeds 125 bytes")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.isClosed {
		return io.ErrClosedPipe
	}

	header := make([]byte, 0, 14)
	header = append(header, 0x80|opcode)
	length := len(payload)

	switch {
	case length <= 125:
		header = append(header, byte(0x80|length))
	case length <= math.MaxUint16:
		header = append(header, 0x80|126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(length))
		header = append(header, ext[:]...)
	default:
		header = append(header, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		header = append(header, ext[:]...)
	}

	var maskKey [4]byte
	_, _ = rand.Read(maskKey[:])
	header = append(header, maskKey[:]...)

	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ maskKey[i%4]
	}

	if _, err := c.conn.Write(header); err != nil {
		return fmt.Errorf("write websocket header: %w", err)
	}
	if len(masked) > 0 {
		if _, err := c.conn.Write(masked); err != nil {
			return fmt.Errorf("write websocket payload: %w", err)
		}
	}
	return nil
}

func (c *WSClient) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var head [2]byte
	if _, err := io.ReadFull(c.reader, head[:]); err != nil {
		return false, 0, nil, err
	}

	fin = head[0]&0x80 != 0
	opcode = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}

	if opcode >= opcodeClose {
		if !fin {
			return false, 0, nil, errors.New("fragmented control frame")
		}
		if length > 125 {
			return false, 0, nil, errors.New("oversized control frame")
		}
	}
	if length > math.MaxInt32 {
		return false, 0, nil, errors.New("websocket frame too large")
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.reader, maskKey[:]); err != nil {
			return false, 0, nil, err
		}
	}

	payload = make([]byte, int(length))
	if length > 0 {
		if _, err := io.ReadFull(c.reader, payload); err != nil {
			return false, 0, nil, err
		}
	}

	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	return fin, opcode, payload, nil
}

func headerHasToken(headers http.Header, key, token string) bool {
	want := strings.ToLower(strings.TrimSpace(token))
	for _, value := range headers.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.ToLower(strings.TrimSpace(part)) == want {
				return true
			}
		}
	}
	return false
}

func randomSecWebsocketKey() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return base64.StdEncoding.EncodeToString(raw[:])
}

func websocketAccept(key string) string {
	hash := sha1.Sum([]byte(strings.TrimSpace(key) + websocketMagic))
	return base64.StdEncoding.EncodeToString(hash[:])
}
