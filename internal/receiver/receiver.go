package receiver

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/OrPudding/OrShare/internal/proto"
	tclient "github.com/OrPudding/OrShare/internal/transport/client"
	"github.com/gorilla/websocket"
)

type Options struct {
	Host       string
	Port       int
	AutoAccept bool
}

type Receiver struct {
	opt Options
}

func New(opt Options) *Receiver {
	return &Receiver{opt: opt}
}

func (r *Receiver) Run(ctx context.Context) error {
	conn, resp, err := tclient.DialWSS(r.opt.Host, r.opt.Port)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial wss failed: %w (http=%s)", err, resp.Status)
		}
		return fmt.Errorf("dial wss failed: %w", err)
	}
	defer conn.Close()

	log.Printf("WSS connected: %s:%d\n", r.opt.Host, r.opt.Port)

	type sendReq struct {
		taskId    string
		payload   map[string]interface{}
		sender    string
		fileName  string
		fileCount int
		totalSize int64
		text      string
	}
	var pending *sendReq
	downloadDone := make(chan error, 1)

	// --- Reader goroutine: the ONLY goroutine that reads from conn ---
	msgCh := make(chan []byte, 16)
	errCh := make(chan error, 1)

	go func() {
		defer close(msgCh)
		for {
			_, b, err := conn.ReadMessage()
			if err != nil {
				// one-shot send; if main already returned, just drop
				select {
				case errCh <- err:
				default:
				}
				return
			}
			select {
			case msgCh <- b:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case err := <-downloadDone:
			if err != nil {
				return err
			}

			// 下载完成：发 status=ok
			if pending != nil {
				st := proto.MakeStatus(99, pending.taskId, 1, "ok")
				if err := writeMsg(conn, st); err != nil {
					return err
				}
				// 给手机一点时间处理 status（别太长）
				time.Sleep(200 * time.Millisecond)
			}

			// 主动 close，让手机 UI 立刻结束等待
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"),
				time.Now().Add(1*time.Second),
			)
			return nil

		case err := <-errCh:
			return fmt.Errorf("ws read: %w", err)

		case msgBytes, ok := <-msgCh:
			if !ok {
				// reader goroutine exited; error should come from errCh, but just in case
				return fmt.Errorf("ws closed")
			}

			raw := string(msgBytes)
			m, err := proto.Parse(raw)
			if err != nil {
				log.Printf("parse error: %v, raw=%q\n", err, raw)
				continue
			}

			if strings.ToLower(m.Type) != "action" {
				continue
			}

			nameLower := strings.ToLower(m.Name)
			switch nameLower {
			case "versionnegotiation":
				inVer := 1
				if m.Payload != nil {
					if v, ok := m.Payload["version"].(float64); ok {
						inVer = int(v)
					}
				}
				curVer := inVer
				if curVer > 1 {
					curVer = 1
				}
				ackPayload := map[string]interface{}{
					"version":     curVer,
					"threadLimit": 32,
				}
				if err := writeMsg(conn, proto.MakeAck(m, ackPayload)); err != nil {
					return err
				}

			case "sendrequest":
				if err := writeMsg(conn, proto.MakeAck(m, nil)); err != nil {
					return err
				}
				if m.Payload == nil {
					continue
				}

				taskId := ""
				if v, ok := m.Payload["taskId"].(string); ok && v != "" {
					taskId = v
				} else if v, ok := m.Payload["id"].(string); ok && v != "" {
					taskId = v
				}
				if taskId == "" {
					taskId = "unknown"
				}

				senderName, _ := m.Payload["senderName"].(string)
				fileName, _ := m.Payload["fileName"].(string)

				fileCount := 1
				if v, ok := m.Payload["fileCount"].(float64); ok {
					fileCount = int(v)
				}
				totalSize := int64(0)
				if v, ok := m.Payload["totalSize"].(float64); ok {
					totalSize = int64(v)
				}
				textContent, _ := m.Payload["textContent"].(string)

				pending = &sendReq{
					taskId:    taskId,
					payload:   m.Payload,
					sender:    senderName,
					fileName:  fileName,
					fileCount: fileCount,
					totalSize: totalSize,
					text:      textContent,
				}

				// Display incoming request to the user.
				fmt.Printf("\nIncoming transfer from %s\n", senderName)
				if fileCount > 1 {
					fmt.Printf("Files: %d\n", fileCount)
					if fileName != "" {
						fmt.Printf("First file: %s\n", fileName)
					}
				} else if fileName != "" {
					fmt.Printf("File: %s\n", fileName)
				}
				if totalSize > 0 {
					fmt.Printf("Total size: %d bytes\n", totalSize)
				}
				if textContent != "" {
					fmt.Printf("Text: %s\n", textContent)
				}

				accept := r.opt.AutoAccept
				if !r.opt.AutoAccept {
					fmt.Print("Accept transfer? [y/N]: ")
					var ans string
					fmt.Scanln(&ans)
					if strings.ToLower(strings.TrimSpace(ans)) == "y" {
						accept = true
					}
				}
				if !accept {
					st := proto.MakeStatus(99, taskId, 3, "user refuse")
					_ = writeMsg(conn, st)
					return fmt.Errorf("user refused transfer")
				}

				// Download in background; completion triggers downloadDone case
				go func(taskId string) {
					fmt.Println("Starting download...")

					// 1) temp dir
					tmpDir, err := os.MkdirTemp("", "orshare-*")
					if err != nil {
						downloadDone <- fmt.Errorf("mkdtemp: %w", err)
						return
					}
					defer os.RemoveAll(tmpDir)

					// 2) download zip into tmpDir
					zipPath, n, err := tclient.DownloadZipWithProgress(
						r.opt.Host,
						r.opt.Port,
						taskId,
						tmpDir,
						func(downloaded, total int64) {
							if total > 0 {
								percent := float64(downloaded) / float64(total) * 100
								fmt.Printf("\rProgress: %.1f%% (%d/%d bytes)", percent, downloaded, total)
							} else {
								fmt.Printf("\rProgress: %d bytes", downloaded)
							}
						},
					)
					fmt.Println()

					if err != nil {
						downloadDone <- err
						return
					}

					fmt.Printf("Download completed (temp): %s (%d bytes)\n", zipPath, n)

					// 3) extract to Downloads
					outFiles, err := extractZipToUserDownloads(zipPath)
					if err != nil {
						downloadDone <- err
						return
					}

					fmt.Printf("Extracted %d item(s) to Downloads:\n", len(outFiles))
					for _, p := range outFiles {
						fmt.Printf("  - %s\n", p)
					}

					downloadDone <- nil
				}(taskId)

			case "status":
				if err := writeMsg(conn, proto.MakeAck(m, nil)); err != nil {
					return err
				}
				if m.Payload != nil {
					typ := 0
					reason := ""
					if v, ok := m.Payload["type"].(float64); ok {
						typ = int(v)
					}
					if v, ok := m.Payload["reason"].(string); ok {
						reason = v
					}
					if typ == 3 && reason == "user refuse" {
						return fmt.Errorf("remote refused")
					}
				}

			default:
				if err := writeMsg(conn, proto.MakeAck(m, nil)); err != nil {
					return err
				}
			}
		}
	}
}

func writeMsg(conn *websocket.Conn, m *proto.WebSocketMessage) error {
	text, err := m.Encode(nil)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, []byte(text))
}
func userDownloadsDir() (string, error) {
	// 优先 XDG（很多 Linux 桌面正确）
	cmd := exec.Command("bash", "-lc", "xdg-user-dir DOWNLOAD 2>/dev/null || true")
	out, _ := cmd.CombinedOutput()
	p := strings.TrimSpace(string(out))
	if p != "" && filepath.IsAbs(p) {
		return p, nil
	}

	// fallback: ~/Downloads
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}

var leadingIndexDir = regexp.MustCompile(`^\d+/`) // "0/" "1/" ...

func sanitizeZipEntryName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "/")
	// 去掉 "0/" 这类前缀目录（你提到的结构）
	name = leadingIndexDir.ReplaceAllString(name, "")
	// 如果去掉后为空，就返回空
	return name
}

func extractZipToUserDownloads(zipPath string) ([]string, error) {
	downloadsDir, err := userDownloadsDir()
	if err != nil {
		return nil, fmt.Errorf("get downloads dir: %w", err)
	}

	dstDir := filepath.Join(downloadsDir, "OrShare")

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir target: %w", err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	var written []string

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}

		rel := sanitizeZipEntryName(f.Name)
		if rel == "" {
			continue
		}

		outPath := filepath.Join(dstDir, rel)

		// 防 Zip Slip：目标必须在 dstDir 内
		cleanDst := filepath.Clean(dstDir) + string(os.PathSeparator)
		cleanOut := filepath.Clean(outPath)
		if !strings.HasPrefix(cleanOut, cleanDst) {
			return nil, fmt.Errorf("zip entry escapes target dir: %q", f.Name)
		}

		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir parent: %w", err)
		}

		outPath = ensureNoClobber(outPath)

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open entry: %w", err)
		}

		w, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("create file: %w", err)
		}

		if _, err := io.Copy(w, rc); err != nil {
			w.Close()
			rc.Close()
			return nil, fmt.Errorf("write file: %w", err)
		}

		_ = w.Close()
		_ = rc.Close()

		written = append(written, outPath)
	}

	return written, nil
}

func ensureNoClobber(path string) string {
	if _, err := os.Stat(path); err != nil {
		return path // not exists
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	dir := filepath.Dir(path)

	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(cand); err != nil {
			return cand
		}
	}
	// 实在不行就加时间戳
	return filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, time.Now().Unix(), ext))
}
