package proxy

import (
	"bytes"
	"encoding/json"
)

// rewriteModelInChunk processes a raw SSE byte chunk (may contain multiple
// lines) and replaces the "model" field value in every "data: {JSON}" line
// with targetModel. All other lines pass through unchanged.
// Returns chunk unmodified if targetModel is empty.
func rewriteModelInChunk(chunk []byte, targetModel string) []byte {
	if targetModel == "" {
		return chunk
	}
	lines := bytes.Split(chunk, []byte("\n"))
	modified := false
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data: {")) {
			continue
		}
		data := bytes.TrimPrefix(trimmed, []byte("data: "))
		rewritten := rewriteModelField(data, targetModel)
		if !bytes.Equal(rewritten, data) {
			lines[i] = append([]byte("data: "), rewritten...)
			modified = true
		}
	}
	if !modified {
		return chunk
	}
	return bytes.Join(lines, []byte("\n"))
}

// rewriteModelInBody replaces the "model" field in a complete JSON response
// body (non-streaming). Returns body unmodified if targetModel is empty.
func rewriteModelInBody(body []byte, targetModel string) []byte {
	if targetModel == "" {
		return body
	}
	return rewriteModelField(body, targetModel)
}

// rewriteModelField sets data["model"] = targetModel. Uses
// map[string]json.RawMessage to preserve all other fields and their exact
// raw values. Returns data unchanged on any parse error.
func rewriteModelField(data []byte, targetModel string) []byte {
	if targetModel == "" || !bytes.Contains(data, []byte(`"model"`)) {
		return data
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return data
	}
	if _, ok := obj["model"]; !ok {
		return data
	}
	modelJSON, err := json.Marshal(targetModel)
	if err != nil {
		return data
	}
	obj["model"] = modelJSON
	out, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return out
}
