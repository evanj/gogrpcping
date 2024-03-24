package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/evanj/gogrpcping/echopb"
	"github.com/evanj/hacks/trivialstats"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const echoMessage = "ping"

type echoServer struct {
	echopb.UnimplementedEchoServer
}

func newEchoServer() *echoServer {
	return &echoServer{echopb.UnimplementedEchoServer{}}
}

func (s *echoServer) Echo(ctx context.Context, request *echopb.EchoRequest) (*echopb.EchoResponse, error) {
	resp := &echopb.EchoResponse{
		Output: request.Input,
	}
	return resp, nil
}

func main() {
	grpcPort := flag.Int("grpcPort", 8001, "port for gRPC echo requests")
	tcpPort := flag.Int("tcpPort", 8002, "port for TCP echo requests")
	listenAddr := flag.String("listenAddr", "localhost", "listening address: use empty to listen on all devices")
	interRunSleep := flag.Duration("interRunSleep", 10*time.Second, "time to sleep between runs")
	measurements := flag.Int("measurements", 10000, "number of pings to measure")
	podIPEnvVar := flag.String("podIPEnvVar", "POD_IP",
		"environment variable with IP address to exclude")
	remoteAddr := flag.String("remoteAddr", "gogrpcping-service", "DNS name to look up to find other addresses")
	serviceDiscoverySleep := flag.Duration("serviceDiscoverySleep", time.Second,
		"time to sleep to give k8s DNS time to update")
	flag.Parse()

	slog.Info("starting TCP and gRPC servers ...",
		slog.String("listenAddr", *listenAddr),
		slog.Int("grpcPort", *grpcPort),
		slog.Int("tcpPort", *tcpPort))

	err := startTCPEchoListener(*listenAddr, *tcpPort)
	if err != nil {
		panic(err)
	}

	err = startGRPCEchoServer(*listenAddr, *grpcPort)
	if err != nil {
		panic(err)
	}

	localhostTCPClient, err := newTCPEchoClient("localhost", *tcpPort)
	if err != nil {
		panic(err)
	}
	localhostGRPCClient, err := newGRPCEchoClient("localhost", *grpcPort)
	if err != nil {
		panic(err)
	}

	// wait for kubernetes DNS to update hopefully
	slog.Info("sleeping to give k8s DNS time to update",
		slog.Duration("serviceDiscoverySleep", *serviceDiscoverySleep))
	time.Sleep(*serviceDiscoverySleep)

	selfAddress := os.Getenv(*podIPEnvVar)
	slog.Info("self address",
		slog.String("podIPEnvVar", *podIPEnvVar),
		slog.String("selfAddress", selfAddress))

	addrs, err := net.LookupHost(*remoteAddr)
	if err != nil {
		panic(err)
	}
	slog.Info("found remote addresses",
		slog.String("remoteAddr", *remoteAddr),
		slog.String("addrs", strings.Join(addrs, ",")))

	// remove self address from the list
	for i, addr := range addrs {
		if addr == selfAddress {
			addrs = slices.Delete(addrs, i, i+1)
			break
		}
	}

	slog.Info("final addresses",
		slog.String("addrs", strings.Join(addrs, ",")))

	type namedClients struct {
		name   string
		client echoClient
	}
	clients := []namedClients{
		{"localhost_tcp", localhostTCPClient},
		{"localhost_grpc", localhostGRPCClient},
	}

	for _, addr := range addrs {
		tcpClient, err := newTCPEchoClient(addr, *tcpPort)
		if err != nil {
			panic(err)
		}
		grpcClient, err := newGRPCEchoClient(addr, *grpcPort)
		if err != nil {
			panic(err)
		}
		clients = append(clients, namedClients{"tcp_" + addr, tcpClient})
		clients = append(clients, namedClients{"grpc_" + addr, grpcClient})
	}

	// run the test forever
	for {
		for _, client := range clients {
			latencyNanos, err := measureEchoLatencyNanos(client.client, *measurements)
			if err != nil {
				slog.Error("failed running echo timing test", slog.String("error", err.Error()))
				panic(err)
			}
			slog.Info("echo measurement",
				slog.String("client", client.name),
				slog.Int("measurements", *measurements),
				slog.Duration("p50", time.Duration(latencyNanos.P50)),
				slog.Duration("p90", time.Duration(latencyNanos.P90)),
				slog.Duration("p95", time.Duration(latencyNanos.P95)),
				// slog.Duration("p99", time.Duration(latencyNanos.P99)),
			)
		}

		time.Sleep(*interRunSleep)
	}
}

// measureEchoLatencyNanos returns the latency in nanoseconds.
func measureEchoLatencyNanos(client echoClient, measurements int) (trivialstats.DistributionStats, error) {
	latencyNanos := trivialstats.NewDistribution()
	for i := 0; i < measurements; i++ {
		start := time.Now()
		err := client.Echo(context.Background(), echoMessage)
		end := time.Now()
		if err != nil {
			return trivialstats.DistributionStats{}, err
		}
		elapsed := end.Sub(start)
		latencyNanos.Add(elapsed.Nanoseconds())
	}
	return latencyNanos.Stats(), nil
}

type echoClient interface {
	Echo(ctx context.Context, message string) error
}

type tcpEchoClient struct {
	conn net.Conn
	buf  []byte
}

func newTCPEchoClient(addr string, port int) (*tcpEchoClient, error) {
	conn, err := net.Dial("tcp", addr+":"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	return &tcpEchoClient{conn, make([]byte, 0, 4096)}, nil
}

func (c *tcpEchoClient) Echo(ctx context.Context, message string) error {
	c.buf = append(c.buf[:0], message...)
	c.buf = append(c.buf, '\n')
	n, err := c.conn.Write(c.buf)
	if err != nil {
		return err
	}
	if n != len(message)+1 {
		panic("wtf")
	}
	n, err = io.ReadFull(c.conn, c.buf)
	if err != nil {
		return err
	}
	if n != len(message)+1 {
		panic("wtf")
	}
	return nil
}

type grpcEchoClient struct {
	client echopb.EchoClient
}

func newGRPCEchoClient(addr string, port int) (*grpcEchoClient, error) {
	conn, err := grpc.Dial(addr+":"+strconv.Itoa(port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &grpcEchoClient{echopb.NewEchoClient(conn)}, nil
}

func (c *grpcEchoClient) Echo(ctx context.Context, message string) error {
	_, err := c.client.Echo(ctx, &echopb.EchoRequest{Input: message})
	return err
}

func startTCPEchoListener(addr string, port int) error {
	lis, err := net.Listen("tcp", addr+":"+strconv.Itoa(port))
	if err != nil {
		return err
	}

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				slog.Error("failed accepting connection", slog.String("error", err.Error()))
				continue
			}

			go handleTCPEchoConnection(conn)
		}
	}()

	return nil
}

func handleTCPEchoConnection(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Bytes()
		line = append(line, '\n')
		_, err := conn.Write(line)
		if err != nil {
			slog.Error("failed to write to connection", slog.String("error", err.Error()))
			return
		}
	}
	err := scanner.Err()
	if err != nil {
		slog.Error("failed reading from connection", slog.String("error", err.Error()))
	}
	err = conn.Close()
	if err != nil {
		slog.Error("failed closing connection", slog.String("error", err.Error()))
	}
}

func startGRPCEchoServer(addr string, port int) error {
	lis, err := net.Listen("tcp", addr+":"+strconv.Itoa(port))
	if err != nil {
		panic(err)
	}

	s := grpc.NewServer()
	echopb.RegisterEchoServer(s, newEchoServer())

	go func() {
		err := s.Serve(lis)
		if err != nil {
			slog.Error("failed serving gRPC", slog.String("error", err.Error()))
			panic(err)
		}
	}()
	return nil
}
