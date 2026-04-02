package hub

import (
	"errors"
	"io"
	"testing"
)

func TestShouldFallbackToPull(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "eof disconnect",
			err:  io.EOF,
			want: false,
		},
		{
			name: "closed network connection",
			err:  errors.New("read tcp 127.0.0.1:1234->127.0.0.1:8080: use of closed network connection"),
			want: false,
		},
		{
			name: "connection reset by peer",
			err:  errors.New("read tcp: connection reset by peer"),
			want: false,
		},
		{
			name: "websocket handshake unauthorized",
			err:  errors.New("websocket handshake status=401 body=unauthorized"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldFallbackToPull(tt.err); got != tt.want {
				t.Fatalf("shouldFallbackToPull() = %v, want %v", got, tt.want)
			}
		})
	}
}
