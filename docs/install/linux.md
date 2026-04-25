# Linux 설치 가이드

## Hub 설치

Hub는 공개 IP가 있는 서버에서 실행합니다. GitHub 웹훅을 받아야 하므로 외부에서 접근 가능해야 합니다.

### 1. 바이너리 다운로드

```bash
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/hub-linux-amd64 -o /usr/local/bin/hub
chmod +x /usr/local/bin/hub

# ARM64 서버라면
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/hub-linux-arm64 -o /usr/local/bin/hub
chmod +x /usr/local/bin/hub
```

### 2. 설정 파일 작성

```bash
mkdir -p /etc/preview
cat > /etc/preview/.env << 'EOF'
GITHUB_WEBHOOK_SECRET=여기에_시크릿_입력
ADMIN_PASSWORD=여기에_비밀번호_입력
PREVIEW_BASE_DOMAIN=yourdomain.com
AGENT_DOWNLOAD_URL=https://github.com/lnyarl/preview/releases/latest/download
DATABASE_URL=sqlite:///var/lib/preview/hub.db
EOF

mkdir -p /var/lib/preview
hub migrate up
```

### 3. systemd 서비스 등록

```bash
cat > /etc/systemd/system/preview-hub.service << 'EOF'
[Unit]
Description=Preview Hub
After=network.target

[Service]
EnvironmentFile=/etc/preview/.env
ExecStart=/usr/local/bin/hub
Restart=always
RestartSec=5
WorkingDirectory=/var/lib/preview

[Install]
WantedBy=multi-user.target
EOF

systemctl enable --now preview-hub
```

### 4. TLS / 리버스 프록시 (Caddy)

미리보기 URL(`pr-N.preview.yourdomain.com`)을 받으려면 와일드카드 DNS와 인증서가 필요합니다.

**DNS 설정:**
```
*.preview.yourdomain.com  →  Hub 서버 IP
yourdomain.com            →  Hub 서버 IP
```

**Caddyfile:**
```
yourdomain.com, *.preview.yourdomain.com {
    tls {
        dns <cloudflare 등 DNS 프로바이더> {
            api_token {env.CF_API_TOKEN}
        }
    }
    reverse_proxy localhost:3000
}
```

> 와일드카드 인증서는 DNS 챌린지가 필요합니다. Caddy의 [DNS 모듈](https://caddyserver.com/docs/automatic-https#dns-challenge) 참고.

---

## Agent 설치 (빌드 머신)

Agent는 Docker가 설치된 어느 리눅스 머신에서든 실행할 수 있습니다.  
Hub에 outbound로 연결하므로 인바운드 포트를 열 필요가 없습니다.

### 1. Hub 대시보드에서 Agent 등록

`https://yourdomain.com/admin` → Agents → Add Agent → 토큰 복사

### 2. 바이너리 다운로드

```bash
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/agent-linux-amd64 -o /usr/local/bin/agent
chmod +x /usr/local/bin/agent

# ARM64라면
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/agent-linux-arm64 -o /usr/local/bin/agent
chmod +x /usr/local/bin/agent
```

### 3. systemd 서비스 등록

```bash
cat > /etc/systemd/system/preview-agent.service << 'EOF'
[Unit]
Description=Preview Agent
After=network.target docker.service

[Service]
ExecStart=/usr/local/bin/agent start \
  --hub-url=wss://yourdomain.com/agent/ws \
  --token=agt_여기에_토큰_입력 \
  --repo-url=https://github.com/org/repo.git \
  --advertise-host=이_머신의_IP
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

systemctl enable --now preview-agent
```

> **Private 저장소**라면 `--repo-url`에 PAT를 포함하세요:  
> `https://사용자명:ghp_TOKEN@github.com/org/repo.git`
