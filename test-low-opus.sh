#!/bin/bash
KEY="lyd-dbc1c846854a70c0023c912c9c56630c95113ac7a49ba6b24d3d3f1571cc1ff6"

echo "=== Testing ag/gemini-3.1-pro-low ==="
curl -s -w "\nHTTP_STATUS: %{http_code}\n" -X POST http://localhost:666/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $KEY" \
  -d '{
    "model": "ag/gemini-3.1-pro-low",
    "messages": [{"role": "user", "content": "What is 2+2? Reply with just the number."}]
  }' --max-time 15

echo -e "\n=== Testing ag/claude-opus-4-6-thinking ==="
curl -s -w "\nHTTP_STATUS: %{http_code}\n" -X POST http://localhost:666/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $KEY" \
  -d '{
    "model": "ag/claude-opus-4-6-thinking",
    "messages": [{"role": "user", "content": "What is 3+3? Reply with just the number."}]
  }' --max-time 15
