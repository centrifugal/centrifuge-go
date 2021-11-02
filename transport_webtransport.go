package centrifuge

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lucas-clemente/quic-go/http3"

	"github.com/centrifugal/protocol"
	"github.com/lucas-clemente/quic-go"
)

type webTransport struct {
	mu             sync.Mutex
	stream         *quic.Stream
	wt             http3.WebTransport
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
	EnableDatagrams    bool
}

func newWebTransport(url string, protocolType protocol.Type, config webTransportConfig) (transport, error) {
	client := &http.Client{
		Transport: &http3.RoundTripper{
			TLSClientConfig:    config.TLSConfig,
			DisableCompression: config.DisableCompression,
			QuicConfig:         config.QUICConfig,
			EnableDatagrams:    config.EnableDatagrams,
			EnableWebTransport: true,
		},
	}

	res, err := client.Get(url)
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
		stream:         &stream,
		wt:             wt,
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

	return (*t.stream).Close()
}

func (t *webTransport) reader() {
	defer func() {
		err := t.Close()
		if err != nil {
			fmt.Printf("error on stream close - [%s]\n", err)
		}
		err = t.wt.Close()
		if err != nil {
			fmt.Printf("error on wt client close - [%s]\n", err)
		}
	}()
	defer close(t.replyCh)

	r := bufio.NewReader(*t.stream)
	for {
		data, err := r.ReadBytes('\n')
		if err != nil {
			fmt.Printf("read error - [%s]\n", err)
			return
		}
		log.Println("<----", strings.Trim(string(data), "\n"))
		decoder := newReplyDecoder(t.protocolType, data)

		reply, err := decoder.Decode()
		if err != nil {
			if err == io.EOF {
				continue
			}

			t.disconnect = &disconnect{Reason: "decode error", Reconnect: false}
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
	data, err := t.commandEncoder.Encode(cmd)
	if err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if timeout > 0 {
		_ = (*t.stream).SetWriteDeadline(time.Now().Add(timeout))
	}

	_, err = (*t.stream).Write(append(data, '\n'))

	if timeout > 0 {
		_ = (*t.stream).SetWriteDeadline(time.Time{})
	}

	return err
}

func (t *webTransport) Read() (*protocol.Reply, *disconnect, error) {
	reply, ok := <-t.replyCh
	if !ok {
		return nil, t.disconnect, io.EOF
	}

	return reply, nil, nil
}
