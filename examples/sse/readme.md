# Example for SSE


## Run centrifugo server

```bash
docker run --name centrifugo -d \
   --ulimit nofile=262144:262144 \
  -v ./config.json:/centrifugo/config.json:ro \
  -p 8000:8000 centrifugo/centrifugo:v5 centrifugo -c config.json

```
`config.json`

```json

{
  "admin": true,
  "uni_sse": true,
  "presence": true,
  "history_size": 0,
  "ping_interval": "120s",
  "pong_timeout": "10s",
  "uni_sse_max_request_body_size": 65536,
  "token_hmac_secret_key": "32c68c03-7e14-4cfb-8f25-3442d56b22ed",
  "admin_password": "1553fccb-0579-4539-a189-885c193af3a2",
  "admin_secret": "c9ca9ae7-41c2-4e66-b367-201643e44ce8",
  "api_key": "34c49b9b-d96d-4d8d-8ca6-b8afd1e1dec5",
  "allowed_origins": ["*"],
  "namespaces": [
    {
      "name": "facts",
      "history_size": 10,
      "history_ttl": "300s"
    }
  ]
}

```

## Run tests

```bash
export SSE_API_KEY=34c49b9b-d96d-4d8d-8ca6-b8afd1e1dec5
export SSE_JWT_KEY=32c68c03-7e14-4cfb-8f25-3442d56b22ed

go run .

```
