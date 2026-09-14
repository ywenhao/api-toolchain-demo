package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	demo "example.com/api-toolchain-demo"
	adminv1 "example.com/api-toolchain-demo/gen/demo/admin/v1"
	userv1 "example.com/api-toolchain-demo/gen/demo/user/v1"
	"example.com/api-toolchain-demo/internal/tasks"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service := tasks.NewStore()
	defer service.Events.Close()
	grpcListener, err := net.Listen("tcp", env("GRPC_ADDR", "127.0.0.1:9091"))
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer()
	userv1.RegisterUserTaskServiceServer(grpcServer, &tasks.UserServer{Store: service})
	adminv1.RegisterAdminTaskServiceServer(grpcServer, &tasks.AdminServer{Store: service})
	reflection.Register(grpcServer)
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- grpcServer.Serve(grpcListener) }()
	defer grpcServer.Stop()
	connection, err := grpc.NewClient(grpcListener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer connection.Close()
	newGateway := func() *runtime.ServeMux {
		return runtime.NewServeMux(
			runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
				MarshalOptions:   protojson.MarshalOptions{EmitUnpopulated: true},
				UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: false},
			}),
			runtime.WithForwardResponseOption(responseStatus),
		)
	}
	userGateway, adminGateway := newGateway(), newGateway()
	if err := userv1.RegisterUserTaskServiceHandler(ctx, userGateway, connection); err != nil {
		return err
	}
	if err := adminv1.RegisterAdminTaskServiceHandler(ctx, adminGateway, connection); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/user/v1/", userGateway)
	mux.Handle("/admin/v1/", adminGateway)
	mux.Handle("GET /user/v1/events", service.Events)
	mux.Handle("GET /admin/v1/events", service.Events)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok","service":"proto-api-lab"}`)
	})
	serveAsset := func(path, contentType string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			data, err := demo.Assets.ReadFile(path)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", contentType)
			w.Write(data)
		}
	}
	mux.HandleFunc("GET /{$}", serveAsset("web/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/user", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /docs/user", serveAsset("web/docs.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /docs/admin", serveAsset("web/docs.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /openapi/user.json", serveAsset("openapi/user.json", "application/json; charset=utf-8"))
	mux.HandleFunc("GET /openapi/admin.json", serveAsset("openapi/admin.json", "application/json; charset=utf-8"))
	mux.HandleFunc("GET /README.md", serveAsset("README.md", "text/plain; charset=utf-8"))
	webFS, err := fs.Sub(demo.Assets, "web")
	if err != nil {
		return err
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(webFS))))
	server := &http.Server{
		Addr: env("HTTP_ADDR", "127.0.0.1:8088"), Handler: http.MaxBytesHandler(mux, 1<<20),
		ReadHeaderTimeout: 5 * time.Second,
		// SSE 是长连接，不设置全局 WriteTimeout；每次写入有独立超时。
	}
	go func() { errorsCh <- server.ListenAndServe() }()
	log.Printf("页面: http://%s | 用户文档: /docs/user | 管理文档: /docs/admin", server.Addr)
	log.Printf("gRPC: %s | Ctrl+C 停止", grpcListener.Addr())
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errorsCh:
	}
	service.Events.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		server.Close()
	}
	grpcServer.GracefulStop()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, grpc.ErrServerStopped) {
		return serveErr
	}
	return nil
}

// gRPC metadata 将 proto 文档中的创建成功状态映射为 HTTP 201。
func responseStatus(ctx context.Context, w http.ResponseWriter, _ proto.Message) error {
	md, ok := runtime.ServerMetadataFromContext(ctx)
	if !ok {
		return nil
	}
	values := md.HeaderMD.Get("x-http-code")
	if len(values) == 0 {
		return nil
	}
	code, err := strconv.Atoi(values[0])
	if err != nil {
		return err
	}
	delete(md.HeaderMD, "x-http-code")
	w.Header().Del("Grpc-Metadata-X-Http-Code")
	w.WriteHeader(code)
	return nil
}
