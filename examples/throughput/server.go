package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "net/http/pprof"

	"github.com/centrifugal/centrifuge"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"google.golang.org/grpc"
)

func handleLog(e centrifuge.LogEntry) {
	log.Printf("%s: %v", e.Message, e.Fields)
}

func authMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Our middleware logic goes here...
		ctx := r.Context()
		ctx = context.WithValue(ctx, centrifuge.CredentialsContextKey, &centrifuge.Credentials{
			UserID: "42",
			Exp:    time.Now().Unix() + 10,
			Info:   []byte(`{"name": "Alexander"}`),
		})
		r = r.WithContext(ctx)
		h.ServeHTTP(w, r)
	})
}

func waitExitSignal(n *centrifuge.Node) {
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		n.Shutdown()
		done <- true
	}()
	<-done
}

func main() {
	cfg := centrifuge.DefaultConfig
	cfg.Namespaces = []centrifuge.ChannelNamespace{
		centrifuge.ChannelNamespace{
			Name: "benchmark",
			ChannelOptions: centrifuge.ChannelOptions{
				Publish: true,
			},
		},
	}

	node := centrifuge.New(cfg)

	node.OnConnect(func(ctx context.Context, client centrifuge.Client, e centrifuge.ConnectEvent) centrifuge.ConnectReply {

		client.OnPublish(func(e centrifuge.PublishEvent) centrifuge.PublishReply {
			return centrifuge.PublishReply{}
		})

		log.Printf("user %s connected via %s with encoding: %d", client.UserID(), client.Transport().Name(), client.Transport().Encoding())
		return centrifuge.ConnectReply{}
	})

	node.SetLogHandler(centrifuge.LogLevelDebug, handleLog)

	if err := node.Run(); err != nil {
		panic(err)
	}

	http.Handle("/connection/websocket", authMiddleware(centrifuge.NewWebsocketHandler(node, centrifuge.WebsocketConfig{})))
	http.Handle("/", http.FileServer(http.Dir("./")))

	go func() {
		if err := http.ListenAndServe(":8000", nil); err != nil {
			panic(err)
		}
	}()

	// Also handle GRPC client connections on :8002.
	authInterceptor := func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		newCtx := context.WithValue(ctx, centrifuge.CredentialsContextKey, &centrifuge.Credentials{
			UserID: "42",
			Exp:    time.Now().Unix() + 10,
			Info:   []byte(`{"name": "Alexander"}`),
		})
		wrapped := grpc_middleware.WrapServerStream(ss)
		wrapped.WrappedContext = newCtx
		return handler(srv, wrapped)
	}
	grpcServer := grpc.NewServer(
		grpc.StreamInterceptor(authInterceptor),
	)
	centrifuge.RegisterGRPCServerClient(node, grpcServer, centrifuge.GRPCClientServiceConfig{})
	go func() {
		listener, _ := net.Listen("tcp", ":8001")
		if err := grpcServer.Serve(listener); err != nil {
			log.Fatalf("Serve GRPC: %v", err)
		}
	}()

	waitExitSignal(node)
	fmt.Println("exiting")
}
