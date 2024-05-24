package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/evanj/gogrpcping/echopb"
	"github.com/evanj/hacks/trivialstats"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const echoMessage = "ping"
const tcpInitialBufferBytes = 4096

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
	listenAddr := flag.String("listenAddr", "localhost",
		"listening address: use empty to listen on all devices")
	interRunSleep := flag.Duration("interRunSleep", 10*time.Second, "time to sleep between runs")
	measurements := flag.Int("measurements", 10000, "number of pings to measure")
	podIPEnvVar := flag.String("podIPEnvVar", "POD_IP",
		"environment variable with IP address to exclude")
	remoteAddr := flag.String("remoteAddr", "", "IP/DNS name for remote endpoints")
	remoteLookupSleep := flag.Duration("remoteLookupSleep", 0,
		"sleep before looking up/connecting to remoteAddr: give time to start")
	throughputGoroutines := flag.Int("throughputGoroutines", 0,
		"if set: run clients that make requests in a loop as quickly as possible")
	throughputTime := flag.Duration("throughputTime", time.Minute,
		"run the throughput test for this long")

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

	if *throughputGoroutines > 0 {
		err = runThroughputTest(*remoteAddr, *tcpPort, *grpcPort, *throughputGoroutines, *throughputTime)
		if err != nil {
			panic(err)
		}
		slog.Info("finished throughput test; sleeping")
		time.Sleep(24 * time.Hour)
	}

	localhostTCPClient, err := newTCPEchoClient("localhost", *tcpPort)
	if err != nil {
		panic(err)
	}
	localhostGRPCClient, err := newGRPCEchoClient("localhost", *grpcPort)
	if err != nil {
		panic(err)
	}

	clients := []namedClients{
		{"localhost_tcp", localhostTCPClient},
		{"localhost_grpc", localhostGRPCClient},
	}

	if *remoteAddr != "" {
		remoteClients, err := connectRemoteClients(
			*remoteAddr, *remoteLookupSleep, *podIPEnvVar, *tcpPort, *grpcPort)
		if err != nil {
			panic(err)
		}
		clients = append(clients, remoteClients...)
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
				slog.Int64("measurements", latencyNanos.Count),
				slog.Duration("p1", time.Duration(latencyNanos.P1)),
				slog.Duration("p25", time.Duration(latencyNanos.P25)),
				slog.Duration("p50", time.Duration(latencyNanos.P50)),
				slog.Duration("p75", time.Duration(latencyNanos.P75)),
				slog.Duration("p90", time.Duration(latencyNanos.P90)),
				slog.Duration("p95", time.Duration(latencyNanos.P95)),
				slog.Duration("p99", time.Duration(latencyNanos.P99)),
			)
		}

		time.Sleep(*interRunSleep)
	}
}

func runThroughputTest(
	remoteAddr string, tcpPort int, grpcPort int, goroutines int, measureDuration time.Duration,
) error {

	addrs, err := net.LookupHost(remoteAddr)
	if err != nil {
		return err
	}
	slog.Info("found remote addresses",
		slog.String("remoteAddr", remoteAddr),
		slog.String("addrs", strings.Join(addrs, ",")))

	for _, ipAddr := range addrs {
		var clients []echoClient
		for i := 0; i < goroutines; i++ {
			tcpClient, err := newTCPEchoClient(ipAddr, tcpPort)
			if err != nil {
				return err
			}
			clients = append(clients, tcpClient)
		}

		slog.Info("starting TCP test",
			slog.String("ipAddr", ipAddr), slog.Int("goroutines", goroutines))
		requestsPerSecond, err := measureEchoThroughput(clients, measureDuration)
		if err != nil {
			return err
		}
		slog.Info("TCP throughput results",
			slog.String("ipAddr", ipAddr),
			slog.Float64("requestsPerSecond", requestsPerSecond),
		)

		clients = clients[:0]
		for i := 0; i < goroutines; i++ {
			grpcClient, err := newGRPCEchoClient(ipAddr, grpcPort)
			if err != nil {
				return err
			}
			clients = append(clients, grpcClient)
		}

		slog.Info("starting gRPC test",
			slog.String("ipAddr", ipAddr), slog.Int("goroutines", goroutines))
		requestsPerSecond, err = measureEchoThroughput(clients, measureDuration)
		if err != nil {
			return err
		}
		slog.Info("gRPC throughput results",
			slog.String("remoteAddr", remoteAddr),
			slog.Float64("requestsPerSecond", requestsPerSecond),
		)
	}

	return nil
}

func measureEchoThroughput(clients []echoClient, measureDuration time.Duration) (float64, error) {
	stopWork := &atomic.Bool{}

	goroutineRequests := make(chan int)
	for _, client := range clients {
		go func() {
			requests := 0
			for {
				// poll to see if we should exit
				if stopWork.Load() {
					break
				}

				err := client.Echo(context.Background(), echoMessage)
				if err != nil {
					slog.Error("failed sending echo; stopping test",
						slog.String("error", err.Error()))
					return
				}
				requests++
			}
			goroutineRequests <- requests
		}()
	}

	time.Sleep(measureDuration)
	stopWork.Store(true)

	totalRequests := 0
	for i := 0; i < len(clients); i++ {
		totalRequests += <-goroutineRequests
	}

	requestsPerSecond := float64(totalRequests) / measureDuration.Seconds()
	return requestsPerSecond, nil
}

type namedClients struct {
	name   string
	client echoClient
}

func connectRemoteClients(remoteAddr string, remoteLookupSleep time.Duration, podIPEnvVar string,
	tcpPort int, grpcPort int,
) ([]namedClients, error) {

	// wait for remote hosts
	if remoteLookupSleep > 0 {
		slog.Info("sleeping before connecting to remote hosts ...",
			slog.Duration("remoteLookupSleep", remoteLookupSleep))
		time.Sleep(remoteLookupSleep)
	}

	addrs, err := net.LookupHost(remoteAddr)
	if err != nil {
		return nil, err
	}
	slog.Info("found remote addresses",
		slog.String("remoteAddr", remoteAddr),
		slog.String("addrs", strings.Join(addrs, ",")))

	if podIPEnvVar != "" {
		selfAddress := os.Getenv(podIPEnvVar)
		if selfAddress == "" {
			slog.Warn("self address not found; keeping all remote hosts",
				slog.String("podIPEnvVar", podIPEnvVar))
		} else {
			// remove self address from the list
			found := false
			for i, addr := range addrs {
				if addr == selfAddress {
					addrs = slices.Delete(addrs, i, i+1)
					found = true
					break
				}
			}
			if !found {
				slog.Warn("self address not found in remoteAddr: ignoring",
					slog.String("podIPEnvVar", podIPEnvVar),
					slog.String("selfAddress", selfAddress),
					slog.String("addrs", strings.Join(addrs, ",")))
			} else {
				slog.Info("removed self address from remote addresses",
					slog.String("podIPEnvVar", podIPEnvVar),
					slog.String("selfAddress", selfAddress),
					slog.String("addrs", strings.Join(addrs, ",")))
			}
		}
	}

	var clients []namedClients
	for _, addr := range addrs {
		tcpClient, err := newTCPEchoClient(addr, tcpPort)
		if err != nil {
			return nil, err
		}
		grpcClient, err := newGRPCEchoClient(addr, grpcPort)
		if err != nil {
			return nil, err
		}
		clients = append(clients, namedClients{"tcp_" + addr, tcpClient})
		clients = append(clients, namedClients{"grpc_" + addr, grpcClient})
	}
	return clients, nil
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
	return &tcpEchoClient{conn, make([]byte, 0, tcpInitialBufferBytes)}, nil
}

func (c *tcpEchoClient) Echo(ctx context.Context, message string) error {
	c.buf = append(c.buf[:0], message...)
	c.buf = append(c.buf, '\n')
	n, err := c.conn.Write(c.buf)
	if err != nil {
		return err
	}
	if n != len(message)+1 {
		// Should be impossible: Write must return an error if it returns a short write
		// but this does test that we created the buffer correctly
		panic(fmt.Sprintf("tcp echo: must write len(message)+1=%d ; wrote %d", len(message)+1, n))
	}
	n, err = io.ReadFull(c.conn, c.buf)
	if err != nil {
		return err
	}
	if n != len(message)+1 {
		panic(fmt.Sprintf("tcp echo: expected to read %d bytes in reply; read %d; c.buf=%#v",
			len(message)+1, n, string(c.buf[:n])))
	}
	return nil
}

type grpcEchoClient struct {
	client echopb.EchoClient
}

func newGRPCEchoClient(addr string, port int) (*grpcEchoClient, error) {
	conn, err := grpc.NewClient(addr+":"+strconv.Itoa(port),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
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
