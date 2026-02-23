package upstream

import (
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestMergeIncludesDedupeAndReasoning(t *testing.T) {
	got := mergeIncludes([]string{"foo", "reasoning.encrypted_content", "foo", "bar"}, true)
	want := []string{"foo", "reasoning.encrypted_content", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMergeIncludesNoReasoning(t *testing.T) {
	got := mergeIncludes([]string{"foo", "bar", "foo"}, false)
	want := []string{"foo", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDumpUpstreamResponsePreservesErrorBody(t *testing.T) {
	originalStderr := os.Stderr
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = stderrW
	t.Cleanup(func() { os.Stderr = originalStderr })

	const bodyText = `{"error":"bad"}`
	resp := &http.Response{
		StatusCode: 400,
		Status:     "400 Bad Request",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(bodyText)),
	}

	c := &Client{Debug: true}
	c.dumpUpstreamResponse(resp)

	restoredBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(restoredBody) != bodyText {
		t.Fatalf("restored body: got %q, want %q", string(restoredBody), bodyText)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close restored body: %v", err)
	}

	if err := stderrW.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	rawDump, err := io.ReadAll(stderrR)
	if err != nil {
		t.Fatalf("read stderr dump: %v", err)
	}
	if err := stderrR.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	dumpStr := string(rawDump)
	if !strings.Contains(dumpStr, "===== UPSTREAM RESPONSE BEGIN =====") {
		t.Fatalf("expected upstream response delimiter, got %q", dumpStr)
	}
	if !strings.Contains(dumpStr, "===== UPSTREAM RESPONSE BODY status=400 BEGIN =====") {
		t.Fatalf("expected upstream response body delimiter, got %q", dumpStr)
	}
	if !strings.Contains(dumpStr, "===== UPSTREAM RESPONSE BODY status=400 END =====") {
		t.Fatalf("expected upstream response body end delimiter, got %q", dumpStr)
	}
	if !strings.Contains(dumpStr, bodyText) {
		t.Fatalf("expected upstream error body in dump, got %q", dumpStr)
	}
}

func TestDumpUpstreamResponseSSELogsOnlyCompletedEvent(t *testing.T) {
	originalStderr := os.Stderr
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = stderrW
	t.Cleanup(func() { os.Stderr = originalStderr })

	const bodyText = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"data: [DONE]\n\n"
	resp := &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Proto:      "HTTP/2.0",
		ProtoMajor: 2,
		ProtoMinor: 0,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: io.NopCloser(strings.NewReader(bodyText)),
	}

	c := &Client{Debug: true}
	c.dumpUpstreamResponse(resp)

	passthroughBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read passthrough body: %v", err)
	}
	if string(passthroughBody) != bodyText {
		t.Fatalf("passthrough body: got %q, want %q", string(passthroughBody), bodyText)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close passthrough body: %v", err)
	}

	if err := stderrW.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	rawDump, err := io.ReadAll(stderrR)
	if err != nil {
		t.Fatalf("read stderr dump: %v", err)
	}
	if err := stderrR.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	dumpStr := string(rawDump)

	if !strings.Contains(dumpStr, "===== UPSTREAM RESPONSE BODY status=200 BEGIN =====") {
		t.Fatalf("expected upstream response body delimiter, got %q", dumpStr)
	}
	if !strings.Contains(dumpStr, "\"type\":\"response.completed\"") {
		t.Fatalf("expected response.completed in body dump, got %q", dumpStr)
	}
	if strings.Contains(dumpStr, "\"type\":\"response.output_text.delta\"") {
		t.Fatalf("did not expect delta events in body dump, got %q", dumpStr)
	}
}

func TestDumpUpstreamResponseSSEWithoutContentTypeStillFilters(t *testing.T) {
	originalStderr := os.Stderr
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = stderrW
	t.Cleanup(func() { os.Stderr = originalStderr })

	const bodyText = "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\"}}\n\n"
	resp := &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Proto:      "HTTP/2.0",
		ProtoMajor: 2,
		ProtoMinor: 0,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(bodyText)),
	}

	c := &Client{Debug: true}
	c.dumpUpstreamResponse(resp)

	passthroughBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read passthrough body: %v", err)
	}
	if string(passthroughBody) != bodyText {
		t.Fatalf("passthrough body: got %q, want %q", string(passthroughBody), bodyText)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close passthrough body: %v", err)
	}

	if err := stderrW.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	rawDump, err := io.ReadAll(stderrR)
	if err != nil {
		t.Fatalf("read stderr dump: %v", err)
	}
	if err := stderrR.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	dumpStr := string(rawDump)

	if !strings.Contains(dumpStr, "\"type\":\"response.completed\"") {
		t.Fatalf("expected response.completed in body dump, got %q", dumpStr)
	}
	if strings.Contains(dumpStr, "\"type\":\"response.output_text.delta\"") {
		t.Fatalf("did not expect delta events in body dump, got %q", dumpStr)
	}
	if strings.Contains(dumpStr, "event: response.output_text.delta") {
		t.Fatalf("did not expect event prelude lines in body dump, got %q", dumpStr)
	}
}
