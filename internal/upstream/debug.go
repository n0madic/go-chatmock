package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
)

func (c *Client) dumpUpstreamResponse(resp *http.Response) {
	if c == nil || !c.Debug || resp == nil {
		return
	}

	headerDump, err := httputil.DumpResponse(resp, false)
	if err != nil {
		slog.Error("upstream.response.dump.failed", "error", err)
	} else {
		c.writeDebugDumpBlock("UPSTREAM RESPONSE", headerDump)
	}

	if resp.Body != nil {
		title := fmt.Sprintf("UPSTREAM RESPONSE BODY status=%d", resp.StatusCode)
		c.writeDebugDumpBoundary(title, true)
		contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
		mode := debugBodyDumpModeUnknown
		completedOnly := false
		if strings.Contains(contentType, "text/event-stream") {
			mode = debugBodyDumpModeSSE
			completedOnly = true
		}
		resp.Body = &debugDumpReadCloser{
			src:           resp.Body,
			client:        c,
			title:         title,
			completedOnly: completedOnly,
			mode:          mode,
		}
	}
}

func (c *Client) writeDebugDumpBlock(title string, data []byte) {
	c.writeDebugDumpBoundary(title, true)
	if len(data) > 0 {
		c.writeDebugDumpChunk(data)
		if data[len(data)-1] != '\n' {
			c.writeDebugDumpChunk([]byte("\n"))
		}
	}
	c.writeDebugDumpBoundary(title, false)
}

func (c *Client) writeDebugDumpBoundary(title string, begin bool) {
	if c == nil {
		return
	}
	c.dumpMu.Lock()
	defer c.dumpMu.Unlock()

	kind := "END"
	if begin {
		kind = "BEGIN"
	}
	line := "===== " + strings.TrimSpace(title) + " " + kind + " =====\n"
	if _, err := os.Stderr.WriteString(line); err != nil {
		slog.Error("upstream.dump.write.failed", "title", title, "error", err)
	}
}

func (c *Client) writeDebugDumpChunk(data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	c.dumpMu.Lock()
	defer c.dumpMu.Unlock()
	if _, err := os.Stderr.Write(data); err != nil {
		slog.Error("upstream.dump.write.failed", "error", err)
	}
}

type debugDumpReadCloser struct {
	src           io.ReadCloser
	client        *Client
	title         string
	closed        bool
	lastByte      byte
	hasData       bool
	completedOnly bool
	sseBuf        []byte
	probeBuf      []byte
	mode          debugBodyDumpMode
}

type debugBodyDumpMode int

const (
	debugBodyDumpModeUnknown debugBodyDumpMode = iota
	debugBodyDumpModeRaw
	debugBodyDumpModeSSE
)

func (d *debugDumpReadCloser) Read(p []byte) (int, error) {
	if d == nil || d.src == nil {
		return 0, io.EOF
	}
	n, err := d.src.Read(p)
	if n > 0 {
		chunk := p[:n]
		switch d.mode {
		case debugBodyDumpModeSSE:
			d.sseBuf = append(d.sseBuf, chunk...)
			d.flushCompletedEvents(false)
		case debugBodyDumpModeRaw:
			d.writeRawChunk(chunk)
		default:
			d.probeBuf = append(d.probeBuf, chunk...)
			if looksLikeSSEPayload(d.probeBuf) {
				d.mode = debugBodyDumpModeSSE
				d.completedOnly = true
				d.sseBuf = append(d.sseBuf, d.probeBuf...)
				d.probeBuf = nil
				d.flushCompletedEvents(false)
			} else if len(d.probeBuf) >= 4096 || errors.Is(err, io.EOF) {
				d.mode = debugBodyDumpModeRaw
				d.writeRawChunk(d.probeBuf)
				d.probeBuf = nil
			}
		}
	}
	if errors.Is(err, io.EOF) {
		d.flushCompletedEvents(true)
		d.finish()
	}
	return n, err
}

func (d *debugDumpReadCloser) Close() error {
	if d == nil || d.src == nil {
		return nil
	}
	err := d.src.Close()
	d.finish()
	return err
}

func (d *debugDumpReadCloser) finish() {
	if d == nil || d.closed {
		return
	}
	d.closed = true
	if d.client == nil {
		return
	}
	if d.mode == debugBodyDumpModeUnknown && len(d.probeBuf) > 0 {
		d.mode = debugBodyDumpModeRaw
		d.writeRawChunk(d.probeBuf)
		d.probeBuf = nil
	}
	if d.hasData && d.lastByte != '\n' {
		d.client.writeDebugDumpChunk([]byte("\n"))
	}
	d.client.writeDebugDumpBoundary(d.title, false)
}

func (d *debugDumpReadCloser) flushCompletedEvents(final bool) {
	if d == nil || !d.completedOnly {
		return
	}
	for {
		idx := bytes.Index(d.sseBuf, []byte("\n\n"))
		if idx < 0 {
			break
		}
		frame := d.sseBuf[:idx]
		d.sseBuf = d.sseBuf[idx+2:]
		d.handleSSEFrame(frame)
	}
	if final && len(d.sseBuf) > 0 {
		d.handleSSEFrame(d.sseBuf)
		d.sseBuf = nil
	}
}

func (d *debugDumpReadCloser) handleSSEFrame(frame []byte) {
	lines := bytes.Split(frame, []byte{'\n'})
	for _, rawLine := range lines {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(payload, &obj); err != nil {
			continue
		}
		typ, _ := obj["type"].(string)
		if typ != "response.completed" {
			continue
		}
		d.hasData = true
		d.lastByte = '\n'
		if d.client != nil {
			d.client.writeDebugDumpChunk([]byte("data: "))
			d.client.writeDebugDumpChunk(payload)
			d.client.writeDebugDumpChunk([]byte("\n\n"))
		}
	}
}

func (d *debugDumpReadCloser) writeRawChunk(chunk []byte) {
	if d == nil || len(chunk) == 0 {
		return
	}
	d.hasData = true
	d.lastByte = chunk[len(chunk)-1]
	if d.client != nil {
		d.client.writeDebugDumpChunk(chunk)
	}
}

func looksLikeSSEPayload(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return false
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		return true
	}
	if bytes.Contains(data, []byte("\ndata:")) || bytes.Contains(data, []byte("\nevent:")) {
		return true
	}
	return false
}
