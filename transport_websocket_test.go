package centrifuge

import (
	"testing"

	"github.com/gorilla/websocket"
)

func TestExtractDisconnectWebsocket(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantCode      uint32
		wantReconnect bool
	}{
		{
			name:          "message too big must not reconnect",
			err:           &websocket.CloseError{Code: websocket.CloseMessageTooBig, Text: "message too big"},
			wantCode:      disconnectMessageSizeLimit,
			wantReconnect: false,
		},
		{
			name:          "other sub-3000 close codes fall back to transport closed and reconnect",
			err:           &websocket.CloseError{Code: websocket.CloseGoingAway, Text: "going away"},
			wantCode:      connectingTransportClosed,
			wantReconnect: true,
		},
		{
			name:          "JSON reason from server is used as is",
			err:           &websocket.CloseError{Code: 3000, Text: `{"code":3000,"reason":"custom","reconnect":false}`},
			wantCode:      3000,
			wantReconnect: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := extractDisconnectWebsocket(tt.err)
			if d == nil {
				t.Fatal("expected non-nil disconnect")
			}
			if d.Code != tt.wantCode {
				t.Errorf("Code = %d, want %d", d.Code, tt.wantCode)
			}
			if d.Reconnect != tt.wantReconnect {
				t.Errorf("Reconnect = %v, want %v", d.Reconnect, tt.wantReconnect)
			}
		})
	}
}
