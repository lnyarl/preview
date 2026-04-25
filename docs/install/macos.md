# macOS 설치 가이드

## Hub 설치

로컬 맥이나 맥 서버에서 Hub를 실행할 수 있습니다. 외부에서 Webhook을 받으려면 공개 IP나 터널(ngrok 등)이 필요합니다.

### 1. 바이너리 다운로드

```bash
# Apple Silicon (M1/M2/M3)
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/hub-darwin-arm64 -o /usr/local/bin/hub
chmod +x /usr/local/bin/hub

# Intel
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/hub-darwin-amd64 -o /usr/local/bin/hub
chmod +x /usr/local/bin/hub
```

처음 실행 시 Gatekeeper 경고가 나오면:
```bash
xattr -d com.apple.quarantine /usr/local/bin/hub
```

### 2. 설정 파일 작성

```bash
mkdir -p ~/.preview
cat > ~/.preview/.env << 'EOF'
GITHUB_WEBHOOK_SECRET=여기에_시크릿_입력
ADMIN_PASSWORD=여기에_비밀번호_입력
PREVIEW_BASE_DOMAIN=localhost
AGENT_DOWNLOAD_URL=https://github.com/lnyarl/preview/releases/latest/download
DATABASE_URL=sqlite://~/.preview/hub.db
EOF

hub migrate up
```

### 3. 실행

```bash
source ~/.preview/.env && hub
```

### 4. 시작 시 자동 실행 (launchd)

```bash
cat > ~/Library/LaunchAgents/com.preview.hub.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.preview.hub</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/hub</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>GITHUB_WEBHOOK_SECRET</key><string>여기에_시크릿_입력</string>
    <key>ADMIN_PASSWORD</key><string>여기에_비밀번호_입력</string>
    <key>DATABASE_URL</key><string>sqlite:///Users/내_사용자명/.preview/hub.db</string>
    <key>AGENT_DOWNLOAD_URL</key><string>https://github.com/lnyarl/preview/releases/latest/download</string>
  </dict>
  <key>WorkingDirectory</key>
  <string>/Users/내_사용자명/.preview</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/preview-hub.log</string>
  <key>StandardErrorPath</key><string>/tmp/preview-hub.log</string>
</dict>
</plist>
EOF

launchctl load ~/Library/LaunchAgents/com.preview.hub.plist
```

---

## Agent 설치

### 1. Hub 대시보드에서 Agent 등록

`http://localhost:3000/admin` → Agents → Add Agent → 토큰 복사

### 2. 바이너리 다운로드

```bash
# Apple Silicon (M1/M2/M3)
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/agent-darwin-arm64 -o /usr/local/bin/agent
chmod +x /usr/local/bin/agent

# Intel
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/agent-darwin-amd64 -o /usr/local/bin/agent
chmod +x /usr/local/bin/agent
```

### 3. 실행

```bash
agent start \
  --hub-url=ws://localhost:3000/agent/ws \
  --token=agt_여기에_토큰_입력 \
  --repo-url=https://github.com/org/repo.git \
  --advertise-host=$(ipconfig getifaddr en0)
```

### 4. 시작 시 자동 실행 (launchd)

```bash
cat > ~/Library/LaunchAgents/com.preview.agent.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.preview.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/agent</string>
    <string>start</string>
    <string>--hub-url=ws://localhost:3000/agent/ws</string>
    <string>--token=agt_여기에_토큰_입력</string>
    <string>--repo-url=https://github.com/org/repo.git</string>
    <string>--advertise-host=이_머신의_IP</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/preview-agent.log</string>
  <key>StandardErrorPath</key><string>/tmp/preview-agent.log</string>
</dict>
</plist>
EOF

launchctl load ~/Library/LaunchAgents/com.preview.agent.plist
```

로그 확인:
```bash
tail -f /tmp/preview-hub.log
tail -f /tmp/preview-agent.log
```
