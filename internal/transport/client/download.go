package client

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DownloadZip fetches a zip archive from the sender and writes it to outDir.
// It returns the full path of the downloaded file and the number of bytes
// written.  See DownloadZipWithProgress for a variant that reports progress.
func DownloadZip(host string, port int, taskId string, outDir string) (string, int64, error) {
	return DownloadZipWithProgress(host, port, taskId, outDir, nil)
}

// DownloadZipWithProgress behaves like DownloadZip but invokes the provided
// progress callback periodically as data is streamed.  The callback is
// invoked after each chunk is written with the cumulative number of bytes
// downloaded so far and the total Content‑Length as reported by the server
// (which may be -1 if unknown).  Passing a nil progress function disables
// progress reporting.
func DownloadZipWithProgress(host string, port int, taskId string, outDir string, progress func(downloaded, total int64)) (string, int64, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", 0, err
	}

	url := fmt.Sprintf("https://%s:%d/download?taskId=%s", host, port, taskId)

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec
			},
		},
		Timeout: 0,
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", 0, fmt.Errorf("download failed: %s, body=%q", resp.Status, string(b))
	}

	name := fmt.Sprintf("orshare_%s_%d.zip", taskId, time.Now().Unix())
	path := filepath.Join(outDir, name)

	f, err := os.Create(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	var downloaded int64
	buf := make([]byte, 32*1024)
	total := resp.ContentLength
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			wn, werr := f.Write(buf[:n])
			if werr != nil {
				return path, downloaded, werr
			}
			if wn != n {
				return path, downloaded, io.ErrShortWrite
			}
			downloaded += int64(n)
			if progress != nil {
				progress(downloaded, total)
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return path, downloaded, err
		}
	}
	return path, downloaded, nil
}
