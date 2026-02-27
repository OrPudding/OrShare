package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrPudding/OrShare/internal/ble"
	"github.com/OrPudding/OrShare/internal/receiver"
	"github.com/OrPudding/OrShare/internal/transport"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "orshare",
		Short: "OrShare - Linux CN P2P implementation",
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "daemon",
		Short: "Start OrShare daemon",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Starting daemon...")
			hostname, _ := os.Hostname()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Start BLE advertising so that OEM share clients can discover us.
			if err := ble.StartAdvertising(ctx, hostname); err != nil {
				fmt.Fprintf(os.Stderr, "BLE advertising error: %v\n", err)
			}

			var recvMu sync.Mutex

			if err := ble.StartGattServer(ctx, func(p ble.P2pInfo) {
				fmt.Printf("[P2P] info received: ssid=%q psk=%q mac=%q port=%d\n", p.Ssid, p.Psk, p.Mac, p.Port)

				go func(p ble.P2pInfo) {
					recvMu.Lock()
					defer recvMu.Unlock()

					// 1) Connect to DIRECT-* Wi-Fi
					fmt.Println("[P2P] connecting to Wi-Fi...")
					if err := connectWiFiNM(p.Ssid, p.Psk); err != nil {
						fmt.Fprintf(os.Stderr, "[P2P] Wi-Fi connect error: %v\n", err)
						return
					}

					// 2) Give kernel a moment to populate neighbor/route
					time.Sleep(2 * time.Second)

					// 3) Find peer IP by MAC, fallback to common GO IP
					ip, err := findPeerIPByMAC(p.Mac)
					if err != nil {
						fmt.Fprintf(os.Stderr, "[P2P] find peer ip by mac failed: %v; fallback to 192.168.49.1\n", err)
						ip = "192.168.49.1"
					}
					fmt.Printf("[P2P] peer ip=%s\n", ip)

					// 4) Run receiver client against sender (phone)
					r := receiver.New(receiver.Options{
						Host:       ip,
						Port:       p.Port,
						AutoAccept: true,
					})

					fmt.Printf("[P2P] starting receiver: %s:%d\n", ip, p.Port)
					if err := r.Run(ctx); err != nil {
						fmt.Fprintf(os.Stderr, "[P2P] receiver error: %v\n", err)
						return
					}
				}(p)

			}); err != nil {
				fmt.Fprintf(os.Stderr, "GATT server error: %v\n", err)
			}

			// No Wi-Fi LAN discovery broadcast: removed (unreliable on Wi-Fi Direct / routing changes).

			if err := transport.StartWebSocketServer(":8080"); err != nil {
				panic(err)
			}
		},
	})

	var host string
	var port int
	var autoAccept bool

	recvCmd := &cobra.Command{
		Use:   "recv",
		Short: "Receive files from Sender",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Ctrl+C 优雅退出
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sig
				cancel()
			}()

			r := receiver.New(receiver.Options{
				Host:       host,
				Port:       port,
				AutoAccept: autoAccept,
			})

			return r.Run(ctx)
		},
	}

	recvCmd.Flags().StringVar(&host, "host", "", "GO IP address (e.g. 192.168.49.1)")
	recvCmd.Flags().IntVar(&port, "port", 0, "Sender HTTPS/WSS port")
	recvCmd.Flags().BoolVar(&autoAccept, "auto-accept", true, "Auto accept transfer")

	_ = recvCmd.MarkFlagRequired("host")
	_ = recvCmd.MarkFlagRequired("port")

	rootCmd.AddCommand(recvCmd)

	// scan command: discover nearby devices advertising the CatShare service via BLE.
	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for nearby OrShare/CatShare devices via BLE",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Scanning for devices (Ctrl+C to stop)...")
			ctx, cancel := context.WithCancel(context.Background())
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sig
				cancel()
			}()
			return ble.StartScanning(ctx, func(dev ble.DiscoveredDevice) {
				brand := dev.Brand
				if brand == "" {
					brand = "unknown"
				}
				fmt.Printf("Discovered %s (ID: %s, brand: %s, 5GHz: %v)\n", dev.Name, dev.ID, brand, dev.Supports5Ghz)
			})
		},
	}
	rootCmd.AddCommand(scanCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func connectWiFiNM(ssid, psk string) error {
	const maxRetries = 3

	for i := 1; i <= maxRetries; i++ {
		// 1) 先 rescan（每次尝试都先做一次，保证顺序：rescan -> connect）
		rescan := exec.Command("nmcli", "dev", "wifi", "rescan")
		rescanOut, _ := rescan.CombinedOutput()
		if s := strings.TrimSpace(string(rescanOut)); s != "" {
			fmt.Printf("[nmcli] rescan: %s\n", s)
		}

		// 2) 再 connect
		cmd := exec.Command("nmcli", "dev", "wifi", "connect", ssid, "password", psk)
		out, err := cmd.CombinedOutput()
		output := string(out)
		fmt.Printf("[nmcli] attempt=%d/%d %s\n", i, maxRetries, output)

		if err == nil {
			return nil
		}

		// 只在“未发现 SSID / exit status 10”时重试
		lower := strings.ToLower(output)
		isNotFound :=
			strings.Contains(output, "未发现 SSID") ||
				strings.Contains(lower, "no network with ssid") ||
				(strings.Contains(lower, "ssid") && strings.Contains(lower, "not found")) ||
				strings.Contains(err.Error(), "exit status 10")

		if !isNotFound || i == maxRetries {
			return fmt.Errorf("nmcli connect failed: %w (%s)", err, strings.TrimSpace(output))
		}

		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("nmcli connect failed")
}

func findPeerIPByMAC(mac string) (string, error) {
	// Try neighbor table first
	// ip neigh show | grep -i '<mac>' | head -n1 | awk '{print $1}'
	sh := fmt.Sprintf("ip neigh show | grep -i '%s' | head -n1 | awk '{print $1}'", mac)
	cmd := exec.Command("bash", "-lc", sh)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ip neigh failed: %w (%s)", err, string(out))
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("no ip found for mac %s", mac)
	}
	return ip, nil
}
