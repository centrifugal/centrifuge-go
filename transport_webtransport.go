package centrifuge

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lucas-clemente/quic-go/http3"

	"github.com/centrifugal/protocol"
	"github.com/lucas-clemente/quic-go"
)

type webTransport struct {
	mu             sync.Mutex
	stream         quic.Stream
	webTransport   http3.WebTransport
	protocolType   protocol.Type
	commandEncoder protocol.CommandEncoder
	replyCh        chan *protocol.Reply
	config         webTransportConfig
	disconnect     *disconnect
	closed         bool
	closeCh        chan struct{}
}

type webTransportConfig struct {
	TLSConfig          *tls.Config
	QUICConfig         *quic.Config
	DisableCompression bool

	// Header specifies custom HTTP Header to send.
	Header http.Header
}

func newWebTransport(url string, protocolType protocol.Type, config webTransportConfig) (transport, error) {
	client := &http.Client{
		Transport: &http3.RoundTripper{
			TLSClientConfig:    config.TLSConfig,
			DisableCompression: config.DisableCompression,
			QuicConfig:         config.QUICConfig,
			EnableWebTransport: true,
		},
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = config.Header

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wrong status code while connecting to server: %d", res.StatusCode)
	}

	wt, err := res.Body.(http3.WebTransporter).WebTransport()
	if err != nil {
		return nil, err
	}

	stream, err := wt.OpenStream()
	if err != nil {
		return nil, err
	}

	t := &webTransport{
		stream:         stream,
		webTransport:   wt,
		replyCh:        make(chan *protocol.Reply, 128),
		config:         config,
		closeCh:        make(chan struct{}),
		commandEncoder: newCommandEncoder(protocolType),
		protocolType:   protocolType,
	}

	go t.reader()

	return t, nil
}

func (t *webTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.closeCh)

	defer func() {
		err := t.webTransport.Close()
		if err != nil {
			log.Println("error on web transport closing: ", err)
		}
	}()

	return t.stream.Close()
}

func (t *webTransport) reader() {
	defer func() {
		_ = t.Close()
		_ = t.webTransport.Close()
	}()
	defer close(t.replyCh)

	var replyDecoder protocol.StreamReplyDecoder
	if t.protocolType == protocol.TypeJSON {
		replyDecoder = protocol.NewJSONStreamReplyDecoder(t.stream)
	} else {
		replyDecoder = protocol.NewProtobufStreamReplyDecoder(t.stream)
	}
	for {
		reply, err := replyDecoder.Decode()
		if err != nil {
			if err != io.EOF {
				t.disconnect = &disconnect{Reason: "decode error", Reconnect: false}
			}
			return
		}
		select {
		case <-t.closeCh:
			return
		case t.replyCh <- reply:
		default:
			// Can't keep up with server message rate.
			t.disconnect = &disconnect{Reason: "client slow", Reconnect: true}
			return
		}
	}
}

func (t *webTransport) Write(cmd *protocol.Command, timeout time.Duration) error {
	cmdBytes, err := t.commandEncoder.Encode(cmd)
	if err != nil {
		return err
	}
	encoder := protocol.GetDataEncoder(t.protocolType)
	defer protocol.PutDataEncoder(t.protocolType, encoder)

	t.mu.Lock()
	defer t.mu.Unlock()

	if timeout > 0 {
		_ = t.stream.SetWriteDeadline(time.Now().Add(timeout))
	}

	_ = encoder.Encode(cmdBytes)
	_, err = t.stream.Write(encoder.Finish())
	if err != nil {
		return err
	}

	if timeout > 0 {
		_ = t.stream.SetWriteDeadline(time.Time{})
	}

	return nil
}

func (t *webTransport) Read() (*protocol.Reply, *disconnect, error) {
	reply, ok := <-t.replyCh
	if !ok {
		return nil, t.disconnect, io.EOF
	}

	return reply, nil, nil
}
